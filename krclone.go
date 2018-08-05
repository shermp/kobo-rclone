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

var koboModels = []string{"N867", "N709", "N236", "N587", "N437", "N250", "N514", "N204B", "N613", "N705", "N905", "N905B", "N905C"}

// BookMetadata is a struct to store data from a Calibre metadata JSON file
type BookMetadata struct {
	Lpath       string  `json:"lpath"`
	Series      string  `json:"series"`
	SeriesIndex float64 `json:"series_index"`
}

// chkErrFatal prints a message to the Kobo screen, then exits the program
func chkErrFatal(err error, usrMsg string, msgDuration int) {
	if err != nil {
		if usrMsg != "" {
			fbPrintCentred(usrMsg)
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

// fbPrintCentred uses the fbink program to print text on the Kobo screen
func fbPrintCentred(str string) {
	var fbinkOpts gofbink.FBInkConfig
	fbinkOpts.Row = 4
	fbinkOpts.IsCentered = true
	err := gofbink.Print(gofbink.FBFDauto, str, fbinkOpts)
	logErrPrint(err)
}

// getKoboVersion attempts to get the model number of the Kobo device
// we are running on
func getKoboVersion() string {
	ret := ""
	versPath := filepath.Join(onboardMnt, koboDir, "version")
	text, err := ioutil.ReadFile(versPath)
	chkErrFatal(err, "Couldn't get Kobo version. Aborting", 5)
	verString := string(text)
	for _, model := range koboModels {
		if strings.HasPrefix(verString, model) {
			ret = model
			break
		}
	}
	return ret
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

// nickelUSBconnTouch simulates pressing the touch screen to 'press' the 'connect' button
// when 'plugging in' the usb cable.
//
// It replays events captured by /dev/input/event1, which are stored in a model specific
// file.
func nickelUSBconnTouch(koboVer string) {
	touchFilePath := filepath.Join(onboardMnt, krcloneDir, "touchevents/usbconnect/", koboVer)
	inFile, _ := os.OpenFile(touchFilePath, os.O_RDONLY, 0666)
	touchEvent, _ := ioutil.ReadAll(inFile)
	defer inFile.Close()
	outFile, _ := os.OpenFile(koboTouchInput, os.O_WRONLY, os.ModeCharDevice)
	outFile.Write(touchEvent)
	defer outFile.Close()
}

// updateMetadata attempts to update the metadata in the Nickel database
func updateMetadata(koboVer string) {
	fbPrintCentred("Updating Metadata...")
	// Make sure we aren't in the directory we will be attempting to mount/unmount
	os.Chdir("/")
	os.Remove(filepath.Join(onboardMnt, metaLFpath))
	// Open and read the metadata into an array of structs
	calibreMDpath := filepath.Join(onboardMnt, krBookDir, ".metadata.calibre")
	mdFile, err := os.OpenFile(calibreMDpath, os.O_RDONLY, 0666)
	if err != nil {
		fbPrintCentred("Could not open Metadata File... Aborting!")
		mdFile.Close()
		return
	}
	mdJSON, _ := ioutil.ReadAll(mdFile)
	mdFile.Close()
	var metadata []BookMetadata
	json.Unmarshal(mdJSON, &metadata)
	// Process metadata if it exists
	if len(metadata) > 0 {
		nickelUSBplug()
		time.Sleep(3 * time.Second)
		nickelUSBconnTouch(koboVer)
		time.Sleep(3 * time.Second)
		os.MkdirAll(tmpOnboardMnt, 0666)
		// 'Plugging' in the USB and 'connecting' causes Nickel to unmount /mnt/onboard...
		// Let's be naughty and remount it elsewhere so we can access the DB without Nickel interfering
		err := syscall.Mount(internalMemoryDev, tmpOnboardMnt, "vfat", 0, "")
		if err == nil {
			// Attempt to open the DB
			koboDBpath := filepath.Join(tmpOnboardMnt, koboDir, "KoboReader.sqlite")
			koboDSN := "file:" + koboDBpath + "?cache=shared&mode=rw"
			db, err := sql.Open("sqlite3", koboDSN)
			if err != nil {
				fbPrintCentred(err.Error())
				return
			}
			// Create a prepared statement we can reuse
			stmt, err := db.Prepare("UPDATE content SET Series=?, SeriesNumber=? WHERE ContentID LIKE ?")
			if err == nil {
				for _, meta := range metadata {
					// Retrieve the values, and update the relevant records in the DB
					path := meta.Lpath
					series := meta.Series
					seriesIndex := strconv.FormatFloat(meta.SeriesIndex, 'f', -1, 64)
					// Note, these fbPrintCentred statements are for informational and debugging purposes
					fbPrintCentred(path)
					time.Sleep(250 * time.Millisecond)
					fbPrintCentred(series)
					time.Sleep(250 * time.Millisecond)
					fbPrintCentred(seriesIndex)
					time.Sleep(250 * time.Millisecond)
					if path != "" && series != "" && seriesIndex != "" {
						_, err := stmt.Exec(series, seriesIndex, "%"+path)
						if err != nil {
							fbPrintCentred("MD Error")
						} else {
							fbPrintCentred("MD Success")
						}
					}
				}
			} else {
				fbPrintCentred(err.Error())
			}
			db.Close()
			time.Sleep(3 * time.Second) // is this needed?
			// We're done. Better unmount the filesystem before we return control to Nickel
			syscall.Unmount(tmpOnboardMnt, 0)
			time.Sleep(3 * time.Second) // is this needed?
			nickelUSBunplug()
			fbPrintCentred("Metadata updated!")
		} else {
			fbPrintCentred(err.Error())
		}

	}
}

// syncBooks runs the rclone program using the preconfigered configuration file.
func syncBooks(rcBin, rcConf, ksDir, koboVer string) {
	rcRemote := "krclone:"
	fbPrintCentred("Starting Sync... Please wait.")
	syncCmd := exec.Command(rcBin, "sync", rcRemote, ksDir, "--config", rcConf)
	err := syncCmd.Run()
	if err != nil {
		fbPrintCentred("Sync failed. Aborting!")
		return
	}
	// Sync has succeeded. We need Nickel to process the new files, so we simulate
	// a USB connection.
	nickelUSBplug()
	// Simulate the connection and disconnection over 12 seconds, to give Nickel some time...
	for i := 12; i > 0; i-- {
		if i == 8 {
			nickelUSBconnTouch(koboVer)
		}
		msg := fmt.Sprintf("Simulating USB. Disconnectiong in %d s...", i)
		fbPrintCentred(msg)
		time.Sleep(1 * time.Second)
	}
	nickelUSBunplug()
	fbPrintCentred("Done! Please rerun to update metadata.")
	time.Sleep(4 * time.Second)
	// Create the lock file to inform our program to get the metadata on next run
	f, _ := os.Create(filepath.Join(onboardMnt, metaLFpath))
	defer f.Close()
	fbPrintCentred(" ")
}

func main() {
	koboVers := getKoboVersion()
	rcloneBin := filepath.Join(onboardMnt, krcloneDir, "rclone")
	rcloneConfig := filepath.Join(onboardMnt, krcloneDir, "rclone.conf")
	bookdir := filepath.Join(onboardMnt, krBookDir)
	if metadataLockfileExists() {
		updateMetadata(koboVers)

	} else {
		syncBooks(rcloneBin, rcloneConfig, bookdir, koboVers)
	}
}
