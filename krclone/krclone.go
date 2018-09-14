/*
Copywrite 2018 Sherman Perry

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
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	linuxproc "github.com/c9s/goprocinfo/linux"
	_ "github.com/mattn/go-sqlite3"
	"github.com/shermp/go-fbink-v2/gofbink"
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

func internalMemUnmounted() (unmounted bool, err error) {
	mnts, err := linuxproc.ReadMounts("/proc/mounts")
	if err != nil {
		unmounted, err = false, errors.New("mount points unavailable")
		return unmounted, err
	}
	for _, m := range mnts.Mounts {
		if strings.Contains(m.Device, "mmcblk0p3") {
			// Internal memory is mounted.
			unmounted, err = false, nil
			return unmounted, err
		}
	}
	unmounted, err = true, nil
	return unmounted, err
}

func waitForUnmount(approxTimeout int) error {
	iterations := (approxTimeout * 1000) / 250
	for i := 0; i < iterations; i++ {
		time.Sleep(250 * time.Millisecond)
		unmounted, err := internalMemUnmounted()
		if err != nil {
			return err
		} else if unmounted {
			return nil
		}
	}
	return errors.New("internal memory did not unmount")
}

func waitForMount(approxTimeout int) error {
	iterations := (approxTimeout * 1000) / 250
	for i := 0; i < iterations; i++ {
		time.Sleep(250 * time.Millisecond)
		unmounted, err := internalMemUnmounted()
		if err != nil {
			return err
		} else if !unmounted {
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
func fbButtonScan(fb *gofbink.FBInk, pressButton bool) error {
	err := fb.ButtonScan(pressButton, false)
	if err != nil {
		switch err.Error() {
		case "EXIT_FAILURE":
			return errors.New("button not found")
		case "ENOTSUP":
			return errors.New("button press failure")
		case "ENODEV":
			return errors.New("touch event failure")
		}
	}
	return nil
}

// activitySpinner is a little routine to give the end user some feedback on long running processes
func activitySpinner(quit <-chan bool, mtx *sync.Mutex, fb *gofbink.FBInk, msg string) {
	spinStates := []string{"( \\ )", "( | )", "( / )", "( - )", "( \\ )", "( | )", "( / )", "( - )"}
	spinLen := len(spinStates)
	index := 0
	fb.Println(" ")
	for {
		select {
		case <-quit:
			return
		default:
			if index >= spinLen {
				index = 0
			}
			mtx.Lock()
			fb.PrintLastLn(msg, spinStates[index])
			mtx.Unlock()
			index++
			time.Sleep(500 * time.Millisecond)
		}
	}
}

// updateMetadata attempts to update the metadata in the Nickel database
func updateMetadata(ksDir, krcloneDir string, fb *gofbink.FBInk) error {
	// Make sure we aren't in the directory we will be attempting to mount/unmount
	os.Chdir("/")
	os.Remove(filepath.Join(krcloneDir, metaLockFile))
	// Open and read the metadata into an array of structs
	calibreMDpath := filepath.Join(ksDir, ".metadata.calibre")
	mdJSON, err := ioutil.ReadFile(calibreMDpath)
	if err != nil {
		fb.Println("Could not open Metadata File... Aborting!")
		return err
	}
	var metadata []BookMetadata
	json.Unmarshal(mdJSON, &metadata)
	// Process metadata if it exists
	if len(metadata) > 0 {
		fb.Println("Updating Metadata...")
		nickelUSBplug()
		for i := 0; i < 10; i++ {
			err = fbButtonScan(fb, true)
			if i == 9 && err != nil {
				fb.Println("The Connect screen never showed. Aborting!")
				return err
			}
			if err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		// Wait for nickel to unmount the FS
		err = waitForUnmount(10)
		if err != nil {
			fb.Println("The filesystem did not unmount. Aborting!")
			return err
		}
		os.MkdirAll(tmpOnboardMnt, 0666)
		// 'Plugging' in the USB and 'connecting' causes Nickel to unmount /mnt/onboard...
		// Let's be naughty and remount it elsewhere so we can access the DB without Nickel interfering
		err = syscall.Mount(internalMemoryDev, tmpOnboardMnt, "vfat", 0, "")
		if err == nil {
			// Attempt to open the DB
			koboDBpath := filepath.Join(tmpOnboardMnt, ".kobo/KoboReader.sqlite")
			koboDSN := "file:" + koboDBpath + "?cache=shared&mode=rw"
			db, err := sql.Open("sqlite3", koboDSN)
			if err == nil {
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
								log.Println(err)
							}
						}
					}
				} else {
					log.Println(err)
				}
				db.Close()
			} else {
				fb.Println("Could not open database. Metadata not updated")
				log.Println(err)
			}
			// We're done. Better unmount the filesystem before we return control to Nickel
			syscall.Unmount(tmpOnboardMnt, 0)
			// Make sure the FS is unmounted before returning control to Nickel
			err = waitForUnmount(10)
			if err != nil {
				return err
			}
			nickelUSBunplug()
			fb.Println("Metadata update process complete!")
		} else {
			fb.Println("The sneaky remount failed. Aborting!")
			return err
		}

	} else {
		fb.Println("No metadata to update!")
	}
	return nil
}

// syncBooks runs the rclone program using the preconfigered configuration file.
func syncBooks(rcBin, rcConf, rcRemote, ksDir, krcloneDir string, fb *gofbink.FBInk) error {
	if !strings.HasSuffix(rcRemote, ":") {
		rcRemote += ":"
	}
	fb.Println("Starting Sync... Please wait.")
	q := make(chan bool)
	mtx := &sync.Mutex{}
	go activitySpinner(q, mtx, fb, "Waiting for Rclone ")
	syncCmd := exec.Command(rcBin, "sync", rcRemote, ksDir, "--config", rcConf)
	err := syncCmd.Run()
	close(q)
	if err != nil {
		fb.Println("Rclone sync failed. Aborting!")
		return err
	}
	fb.Println("Simulating USB... Please wait.")
	// Sync has succeeded. We need Nickel to process the new files, so we simulate
	// a USB connection. It turns out, 5 seconds may not be nearly long enough. Now
	// set to approx 60 sec
	// Note, the mutex is required so we don't accidentally try to perform a button
	// scan and a print at the same time.
	nickelUSBplug()
	q = make(chan bool)
	go activitySpinner(q, mtx, fb, "Waiting for Nickel ")
	for i := 0; i < 120; i++ {
		mtx.Lock()
		err = fbButtonScan(fb, true)
		mtx.Unlock()
		if i == 119 && err != nil {
			close(q)
			fb.Println("We never got the connect screen! Nickel may not have imported content.")
			log.Println(err)
		}
		if err == nil {
			close(q)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	time.Sleep(5 * time.Second)
	nickelUSBunplug()
	fb.Println("Done! Please rerun to update metadata.")
	err = waitForMount(30)
	if err == nil {
		// Create the lock file to inform our program to get the metadata on next run
		f, _ := os.Create(filepath.Join(krcloneDir, metaLockFile))
		defer f.Close()
		fb.Println(" ")
	} else {
		return err
	}
	return nil
}

func main() {
	// Setup a log file
	logFile, err := os.OpenFile("./krclone.log", os.O_WRONLY|os.O_CREATE, 0664)
	if err != nil {
		fmt.Println("We couldn't open the log file!")
	}
	defer logFile.Close()
	log.SetOutput(logFile)
	// Init FBInk before use
	cfg := gofbink.FBInkConfig{}
	rCfg := gofbink.RestrictedConfig{Fontname: gofbink.IBM, Fontmult: 3}
	fb := gofbink.New(&cfg, &rCfg)
	fb.Open()
	defer fb.Close()
	err = fb.Init(&cfg)
	if err != nil {
		log.Println(err)
		return
	}
	// Discover what directory we are running from
	krcloneDir, err := os.Executable()
	if err != nil {
		fb.Println("Could not get current directory. Aborting!")
		log.Println(err)
		return
	}
	if !strings.HasPrefix(krcloneDir, onboardMnt) {
		krcloneDir = filepath.Join(onboardMnt, krcloneDir)
	}
	krcloneDir, _ = filepath.Split(krcloneDir)
	log.Printf(krcloneDir)

	// Read Config file. TOML is used here. Binary size tradeoff not too bad
	krCfgPath := filepath.Join(krcloneDir, "krclone-cfg.toml")
	var krCfg KRcloneConfig
	if _, err := toml.DecodeFile(krCfgPath, &krCfg); err != nil {
		fb.Println("Couldn't read config file. Aborting!")
		log.Println(err)
		return
	}

	// Run kobo-rclone with our configured settings
	rcloneBin := filepath.Join(krcloneDir, "rclone")
	rcloneConfig := filepath.Join(krcloneDir, krCfg.RcloneCfg)
	bookDir := filepath.Join(onboardMnt, krCfg.KRbookDir)
	if metadataLockfileExists(krcloneDir) {
		err = updateMetadata(bookDir, krcloneDir, fb)
		if err != nil {
			log.Println(err)
		}
	} else {
		err = syncBooks(rcloneBin, rcloneConfig, krCfg.RCremoteName, bookDir, krcloneDir, fb)
		if err != nil {
			log.Println(err)
		}
	}
}
