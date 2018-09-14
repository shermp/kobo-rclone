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
	"container/list"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	linuxproc "github.com/c9s/goprocinfo/linux"
	_ "github.com/mattn/go-sqlite3"
	gofbink "github.com/shermp/go-fbink"
)

// Mountpoints we will be using
const onboardMnt = "/mnt/onboard/"
const tmpOnboardMnt = "/mnt/tmponboard/"

// Internal SD card device
const internalMemoryDev = "/dev/mmcblk0p3"

const metaLockFile = "krmeta.lock"

const krVersionString = "0.2.0"

// This is easier as a global due to the way FBInk works
var fbinkOpts gofbink.FBInkConfig

var fbMsgBuffer = list.New()

// BookMetadata is a struct to store data from a Calibre metadata JSON file
type BookMetadata struct {
	Lpath       string  `json:"lpath"`
	Series      string  `json:"series"`
	SeriesIndex float64 `json:"series_index"`
	Comments    string  `json:"comments"`
}

// KRcloneConfig is a struct to store the kobo-rclone configuration options
type KRcloneConfig struct {
	KRbookDir    string `toml:"krclone_book_dir"`
	RcloneCfg    string `toml:"rclone_config"`
	RCremoteName string `toml:"rclone_remote_name"`
	RCrootDir    string `toml:"rclone_root_dir"`
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
	if fbMsgBuffer.Len() >= 5 {
		elt := fbMsgBuffer.Front()
		fbMsgBuffer.Remove(elt)
	}
	fbMsgBuffer.PushBack(str)
	fbinkOpts.Col = 1
	row := int16(4)
	for m := fbMsgBuffer.Front(); m != nil; m = m.Next() {
		fbinkOpts.Row = row
		rowsPrinted, err := gofbink.Print(gofbink.FBFDauto, m.Value.(string), fbinkOpts)
		if err == nil {
			row += int16(rowsPrinted)
		} else {
			logErrPrint(err)
		}
	}
}

// metadataLockfileExists searches for the existance of a lock file
func metadataLockfileExists(krcloneDir string) bool {
	exists := true
	if _, err := os.Stat(filepath.Join(krcloneDir, metaLockFile)); os.IsNotExist(err) {
		exists = false
	}
	return exists
}

// nickelUSBplug simulates pugging in a USB cable
func nickelUSBplug() {
	nickelHWstatusPipe := "/tmp/nickel-hardware-status"
	nickelPipe, _ := os.OpenFile(nickelHWstatusPipe, os.O_RDWR, os.ModeNamedPipe)
	nickelPipe.WriteString("usb plug add")
	nickelPipe.Close()
}

// nickelUSBunplug simulates unplugging a USB cable
func nickelUSBunplug() {
	nickelHWstatusPipe := "/tmp/nickel-hardware-status"
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
func updateMetadata(ksDir, krcloneDir string) {
	// Make sure we aren't in the directory we will be attempting to mount/unmount
	os.Chdir("/")
	os.Remove(filepath.Join(krcloneDir, metaLockFile))
	// Open and read the metadata into an array of structs
	calibreMDpath := filepath.Join(ksDir, ".metadata.calibre")
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
		fbPrint("Updating Metadata...")
		nickelUSBplug()
		for i := 0; i < 10; i++ {
			err = fbButtonScan(true)
			if i == 9 && err != nil {
				fbPrint(err.Error())
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
			koboDBpath := filepath.Join(tmpOnboardMnt, ".kobo/KoboReader.sqlite")
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

	} else {
		fbPrint("No metadata to update!")
	}
}

// syncBooks runs the rclone program using the preconfigered configuration file.
func syncBooks(rcBin, rcConf, rcRemote, ksDir, krcloneDir string) {
	if !strings.HasSuffix(rcRemote, ":") {
		rcRemote += ":"
	}
	fbPrint("Starting Sync... Please wait.")
	syncCmd := exec.Command(rcBin, "sync", rcRemote, ksDir, "--config", rcConf)
	err := syncCmd.Run()
	if err != nil {
		fbPrint("Sync failed. Aborting!")
		return
	}
	fbPrint("Simulating USB... Please wait.")
	// Sync has succeeded. We need Nickel to process the new files, so we simulate
	// a USB connection. It turns out, 5 seconds may not be nearly long enough. Now
	// set to approx 60 sec
	nickelUSBplug()
	for i := 0; i < 120; i++ {
		err = fbButtonScan(true)
		if i == 119 && err != nil {
			fbPrint(err.Error())
			logErrPrint(err)
			return
		}
		if err == nil {
			break
		}
		if i%2 == 0 {
			msg := fmt.Sprintf("We've been waiting for %d iterations", i)
			fbPrint(msg)
		}
		time.Sleep(500 * time.Millisecond)
	}
	time.Sleep(5 * time.Second)
	nickelUSBunplug()
	fbPrint("Done! Please rerun to update metadata.")
	waitForMount(30)
	// Create the lock file to inform our program to get the metadata on next run
	f, _ := os.Create(filepath.Join(krcloneDir, metaLockFile))
	defer f.Close()
	fbPrint(" ")
}

func main() {
	// Init FBInk before use
	fbinkOpts.IsQuiet = true
	fbinkOpts.Fontmult = 3
	gofbink.Init(gofbink.FBFDauto, fbinkOpts)
	// Discover what directory we are running from
	krcloneDir, err := os.Executable()
	log.Printf(krcloneDir)
	chkErrFatal(err, "Could not get current Directory. Aborting!", 5)
	if !strings.HasPrefix(krcloneDir, onboardMnt) {
		krcloneDir = filepath.Join(onboardMnt, krcloneDir)
	}
	krcloneDir, _ = filepath.Split(krcloneDir)
	log.Printf(krcloneDir)

	// Read Config file. TOML is used here. Binary size tradeoff not too bad
	// here.
	krCfgPath := filepath.Join(krcloneDir, "krclone-cfg.toml")
	var krCfg KRcloneConfig
	if _, err := toml.DecodeFile(krCfgPath, &krCfg); err != nil {
		chkErrFatal(err, "Couldn't read config. Aborting!", 5)
	}

	// Run kobo-rclone with our configured settings
	rcloneBin := filepath.Join(krcloneDir, "rclone")
	rcloneConfig := filepath.Join(krcloneDir, krCfg.RcloneCfg)
	bookDir := filepath.Join(onboardMnt, krCfg.KRbookDir)
	if metadataLockfileExists(krcloneDir) {
		updateMetadata(bookDir, krcloneDir)

	} else {
		syncBooks(rcloneBin, rcloneConfig, krCfg.RCremoteName, bookDir, krcloneDir)
	}
}
