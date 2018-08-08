/* 	Copywrite 2018 Sherman Perry

	This program is free software: you can redistribute it and/or modify
    it under the terms of the GNU Affero General Public License as published by
    the Free Software Foundation, either version 3 of the License, or
    (at your option) any later version.

    This program is distributed in the hope that it will be useful,
    but WITHOUT ANY WARRANTY; without even the implied warranty of
    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
    GNU Affero General Public License for more details.

    You should have received a copy of the GNU Affero General Public License
    along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	linuxproc "github.com/c9s/goprocinfo/linux"
	_ "github.com/mattn/go-sqlite3"
	gofbink "github.com/shermp/go-fbink"
)

const onboardMnt = "/mnt/onboard/"
const tmpOnboardMnt = "/mnt/tmponboard/"
const internalMemoryDev = "/dev/mmcblk0p3"
const krcloneDir = ".adds/kobo-rclone/"
const krcloneTmpDir = "/tmp/krclone/"
const krBookDir = "krclone-books/"
const nickelHWstatusPipe = "/tmp/nickel-hardware-status"
const koboTouchInput = "/dev/input/event1"
const koboDir = ".kobo/"
const metaLFpath = ".adds/kobo-rclone/krmeta.lock"

const krVersionString = "0.1.0"

// This is easier as a global due to the way FBInk works
var fbinkOpts gofbink.FBInkConfig

// BookMetadata is a struct to store data from a Calibre metadata JSON file
type BookMetadata struct {
	Lpath       string  `json:"lpath"`
	Series      string  `json:"series"`
	SeriesIndex float64 `json:"series_index"`
	Comments    string  `json:"comments"`
}

// chkErrFatal prints a message to the Kobo screen, then exits the program
func chkErrFatal(err error, usrMsg string, msgDuration int) {
	if err != nil {
		if usrMsg != "" {
			fbPrint(usrMsg)
			time.Sleep(time.Duration(msgDuration) * time.Second)
		}
		log.Fatal(err)
	}
}

// logErrPrint is a convenience function for logging errors
func logErrPrint(err error) {
	if err != nil {
		log.Print(err)
	}
}

// fbPrint uses the fbink program to print text on the Kobo screen
func fbPrint(str string) {
	fbinkOpts.Row = 4
	err := gofbink.Print(gofbink.FBFDauto, str, fbinkOpts)
	logErrPrint(err)
}

// metadataLockfileExists searches for the existance of a lock file
func metadataLockfileExists() bool {
	exists := true
	if _, err := os.Stat(filepath.Join(onboardMnt, metaLFpath)); os.IsNotExist(err) {
		exists = false
	}
	return exists
}

// nickelUSBplug simulates pugging in a USB cable
func nickelUSBplug() {
	nickelPipe, _ := os.OpenFile(nickelHWstatusPipe, os.O_RDWR, os.ModeNamedPipe)
	nickelPipe.WriteString("usb plug add")
	nickelPipe.Close()
}

// nickelUSBunplug simulates unplugging a USB cable
func nickelUSBunplug() {
	nickelPipe, _ := os.OpenFile(nickelHWstatusPipe, os.O_RDWR, os.ModeNamedPipe)
	nickelPipe.WriteString("usb plug remove")
	nickelPipe.Close()
}

func internalMemUnmounted() bool {
	mnts, err := linuxproc.ReadMounts("/proc/mounts")
	chkErrFatal(err, "Mount status unavailable! Aborting.", 5)
	for _, m := range mnts.Mounts {
		if strings.Contains(m.Device, "mmcblk0p3") {
			// Internal memory is mounted.
			return false
		}
	}
	return true
}

func waitForUnmount(approxTimeout int) error {
	iterations := (approxTimeout * 1000) / 250
	for i := 0; i < iterations; i++ {
		time.Sleep(250 * time.Millisecond)
		if internalMemUnmounted() {
			return nil
		}
	}
	return errors.New("internal memory did not unmount")
}

func waitForMount(approxTimeout int) error {
	iterations := (approxTimeout * 1000) / 250
	for i := 0; i < iterations; i++ {
		time.Sleep(250 * time.Millisecond)
		if !internalMemUnmounted() {
			return nil
		}
	}
	return errors.New("internal memory did not mount")
}

// fbButtonScan simulates pressing the touch screen to 'press' the 'connect' button
// when 'plugging in' the usb cable.
//
// It replays events captured by /dev/input/event1, which are stored in a model specific
// file.
func fbButtonScan(pressButton bool) error {
	err := gofbink.ButtonScan(gofbink.FBFDauto, pressButton, false)
	if err != nil {
		if strings.Compare(err.Error(), "EXIT_FAILURE") == 0 {
			return errors.New("button not found")
		} else if strings.Compare(err.Error(), "ENOTSUP") == 0 {
			return errors.New("button press failure")
		} else if strings.Compare(err.Error(), "ENODEV") == 0 {
			return errors.New("touch event failure")
		}
	}
	return nil
}

// updateMetadata attempts to update the metadata in the Nickel database
func updateMetadata() {
	fbPrint("Updating Metadata...")
	// Make sure we aren't in the directory we will be attempting to mount/unmount
	os.Chdir("/")
	os.Remove(filepath.Join(onboardMnt, metaLFpath))
	// Open and read the metadata into an array of structs
	calibreMDpath := filepath.Join(onboardMnt, krBookDir, ".metadata.calibre")
	mdFile, err := os.OpenFile(calibreMDpath, os.O_RDONLY, 0666)
	if err != nil {
		fbPrint("Could not open Metadata File... Aborting!")
		if mdFile != nil {
			mdFile.Close()
		}
		return
	}
	mdJSON, _ := ioutil.ReadAll(mdFile)
	mdFile.Close()
	var metadata []BookMetadata
	json.Unmarshal(mdJSON, &metadata)
	// Process metadata if it exists
	if len(metadata) > 0 {
		nickelUSBplug()
		for i := 0; i < 10; i++ {
			err = fbButtonScan(true)
			if i == 9 && err != nil {
				fbPrint("Could not press connect button. Aborting!")
				logErrPrint(err)
				return
			}
			if err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		// Wait for nickel to unmount the FS
		err = waitForUnmount(10)
		chkErrFatal(err, "The Filesystem did not unmount. Aborting!", 5)
		os.MkdirAll(tmpOnboardMnt, 0666)
		// 'Plugging' in the USB and 'connecting' causes Nickel to unmount /mnt/onboard...
		// Let's be naughty and remount it elsewhere so we can access the DB without Nickel interfering
		err = syscall.Mount(internalMemoryDev, tmpOnboardMnt, "vfat", 0, "")
		if err == nil {
			// Attempt to open the DB
			koboDBpath := filepath.Join(tmpOnboardMnt, koboDir, "KoboReader.sqlite")
			koboDSN := "file:" + koboDBpath + "?cache=shared&mode=rw"
			db, err := sql.Open("sqlite3", koboDSN)
			if err != nil {
				fbPrint(err.Error())
				return
			}
			// Create a prepared statement we can reuse
			stmt, err := db.Prepare("UPDATE content SET Description=?, Series=?, SeriesNumber=? WHERE ContentID LIKE ?")
			if err == nil {
				for _, meta := range metadata {
					// Retrieve the values, and update the relevant records in the DB
					path := meta.Lpath
					series := meta.Series
					seriesIndex := strconv.FormatFloat(meta.SeriesIndex, 'f', -1, 64)
					description := meta.Comments

					if path != "" {
						_, err := stmt.Exec(description, series, seriesIndex, "%"+path)
						if err != nil {
							fbPrint("MD Error")
						} else {
							fbPrint("MD Success")
						}
					}
				}
			} else {
				fbPrint(err.Error())
			}
			db.Close()
			// We're done. Better unmount the filesystem before we return control to Nickel
			syscall.Unmount(tmpOnboardMnt, 0)
			// Make sure the FS is unmounted before returning control to Nickel
			err = waitForUnmount(10)
			chkErrFatal(err, "The Filesystem did not unmount. Aborting!", 5)
			nickelUSBunplug()
			fbPrint("Metadata updated!")
		} else {
			fbPrint(err.Error())
		}

	}
}

// syncBooks runs the rclone program using the preconfigered configuration file.
func syncBooks(rcBin, rcConf, ksDir string) {
	rcRemote := "krclone:"
	fbPrint("Starting Sync... Please wait.")
	syncCmd := exec.Command(rcBin, "sync", rcRemote, ksDir, "--config", rcConf)
	err := syncCmd.Run()
	if err != nil {
		fbPrint("Sync failed. Aborting!")
		return
	}
	fbPrint("Simulating USB... Please wait.")
	// Sync has succeeded. We need Nickel to process the new files, so we simulate
	// a USB connection.
	nickelUSBplug()
	for i := 0; i < 10; i++ {
		err = fbButtonScan(true)
		if i == 9 && err != nil {
			fbPrint("Could not press connect button. Aborting!")
			logErrPrint(err)
			return
		}
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	time.Sleep(5 * time.Second)
	nickelUSBunplug()
	fbPrint("Done! Please rerun to update metadata.")
	waitForMount(30)
	// Create the lock file to inform our program to get the metadata on next run
	f, _ := os.Create(filepath.Join(onboardMnt, metaLFpath))
	defer f.Close()
	fbPrint(" ")
}

func main() {
	// Init FBInk before use
	fbinkOpts.IsCentered = true
	// fbinkOpts.IsQuiet = true
	gofbink.Init(gofbink.FBFDauto, fbinkOpts)
	rcloneBin := filepath.Join(onboardMnt, krcloneDir, "rclone")
	rcloneConfig := filepath.Join(onboardMnt, krcloneDir, "rclone.conf")
	bookdir := filepath.Join(onboardMnt, krBookDir)
	if metadataLockfileExists() {
		updateMetadata()

	} else {
		syncBooks(rcloneBin, rcloneConfig, bookdir)
	}
}
