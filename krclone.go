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

type fbinkData struct {
	updateLastStr bool
	buttonScan    bool
	printStr      string
}

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

func getNextWorkingIcon(workingIcon string) string {
	if strings.Compare(workingIcon, "(.  )") == 0 {
		return "(.. )"
	} else if strings.Compare(workingIcon, "(.. )") == 0 {
		return "(...)"
	} else {
		return "(.  )"
	}
}

func fbInk(data <-chan fbinkData, btnScanRet chan<- error) {
	// Init FBInk
	fbMsgBuffer := list.New()
	var fbinkOpts gofbink.FBInkConfig
	fbinkOpts.IsQuiet = true
	fbinkOpts.Fontmult = 3
	fbinkOpts.Col = 1
	fbfd := gofbink.Open()
	gofbink.Init(fbfd, fbinkOpts)
	var currData fbinkData
	workingIcon := "(...)"
	fbMsgBuffer.PushBack(" ")
	// Keep looping until the data channel is closed
	for currData = range data {
		if !currData.buttonScan {
			var str string
			if !currData.updateLastStr {
				// We only want to display five message on the screen at any given time
				if fbMsgBuffer.Len() >= 5 {
					elt := fbMsgBuffer.Front()
					fbMsgBuffer.Remove(elt)
				}
			} else {
				workingIcon = getNextWorkingIcon(workingIcon)
				elt := fbMsgBuffer.Back()
				fbMsgBuffer.Remove(elt)
			}

			if currData.updateLastStr {
				str = currData.printStr + " " + workingIcon
			} else {
				str = currData.printStr
			}
			// The latest message appears at the bottom
			fbMsgBuffer.PushBack(str)
			row := int16(4)
			for m := fbMsgBuffer.Front(); m != nil; m = m.Next() {
				fbinkOpts.Row = row
				rowsPrinted, err := gofbink.Print(fbfd, m.Value.(string), fbinkOpts)
				if err == nil {
					row += int16(rowsPrinted)
				}
			}
		} else {
			var retErr error
			err := gofbink.ButtonScan(fbfd, true, false)
			if err != nil {
				if strings.Compare(err.Error(), "EXIT_FAILURE") == 0 {
					retErr = errors.New("button not found")
				} else if strings.Compare(err.Error(), "ENOTSUP") == 0 {
					retErr = errors.New("button press failure")
				} else if strings.Compare(err.Error(), "ENODEV") == 0 {
					retErr = errors.New("touch event failure")
				}
			} else {
				retErr = nil
			}
			btnScanRet <- retErr
		}
	}
	// We're done. Close the framebuffer
	gofbink.Close(fbfd)
}

// logErrPrint is a convenience function for logging errors
func logErrPrint(err error) {
	if err != nil {
		log.Print(err)
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

func internalMemUnmounted() (bool, error) {
	mnts, err := linuxproc.ReadMounts("/proc/mounts")
	if err != nil {
		return false, errors.New("could not read mount state")
	}
	for _, m := range mnts.Mounts {
		if strings.Contains(m.Device, "mmcblk0p3") {
			// Internal memory is mounted.
			return false, nil
		}
	}
	return true, nil
}

func waitForUnmount(approxTimeout int) error {
	iterations := (approxTimeout * 1000) / 250
	for i := 0; i < iterations; i++ {
		time.Sleep(250 * time.Millisecond)
		mountState, err := internalMemUnmounted()
		if err != nil {
			return err
		}
		if mountState {
			return nil
		}
	}
	return errors.New("internal memory did not unmount")
}

func waitForMount(approxTimeout int) error {
	iterations := (approxTimeout * 1000) / 250
	for i := 0; i < iterations; i++ {
		time.Sleep(250 * time.Millisecond)
		mountState, err := internalMemUnmounted()
		if err != nil {
			return err
		}
		if !mountState {
			return nil
		}
	}
	return errors.New("internal memory did not mount")
}

// updateMetadata attempts to update the metadata in the Nickel database
func updateMetadata(ksDir, krcloneDir string, fbDataChannel chan fbinkData, fbBSerr chan error) {
	var fbDat fbinkData
	// Make sure we aren't in the directory we will be attempting to mount/unmount
	os.Chdir("/")
	os.Remove(filepath.Join(krcloneDir, metaLockFile))
	// Open and read the metadata into an array of structs
	calibreMDpath := filepath.Join(ksDir, ".metadata.calibre")
	mdFile, err := os.OpenFile(calibreMDpath, os.O_RDONLY, 0666)
	if err != nil {
		fbDat.printStr = "Could not open Metadata File... Aborting!"
		fbDataChannel <- fbDat
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
		fbDat.printStr = "Updating Metadata..."
		fbDataChannel <- fbDat
		nickelUSBplug()
		for i := 0; i < 10; i++ {
			fbDat.buttonScan = true
			fbDataChannel <- fbDat
			err := <-fbBSerr
			fbDat.buttonScan = false
			if i == 9 && err != nil {
				fbDat.printStr = err.Error()
				fbDataChannel <- fbDat
				logErrPrint(err)
				return
			}
			if err == nil {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		fbDat.buttonScan = false
		// Wait for nickel to unmount the FS
		err = waitForUnmount(10)
		if err != nil {
			fbDat.printStr = err.Error()
			fbDataChannel <- fbDat
			log.Fatal(err)
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
			if err != nil {
				fbDat.printStr = err.Error()
				fbDataChannel <- fbDat
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
							fbDat.printStr = "MD Error!"
							fbDataChannel <- fbDat
						}
					}
				}
			} else {
				fbDat.printStr = err.Error()
				fbDataChannel <- fbDat
			}
			db.Close()
			// We're done. Better unmount the filesystem before we return control to Nickel
			syscall.Unmount(tmpOnboardMnt, 0)
			// Make sure the FS is unmounted before returning control to Nickel
			err = waitForUnmount(10)
			if err != nil {
				fbDat.printStr = err.Error()
				fbDataChannel <- fbDat
				log.Fatal(err)
			}
			nickelUSBunplug()
			fbDat.printStr = "Metadata Updated!"
			fbDataChannel <- fbDat
		} else {
			fbDat.printStr = err.Error()
			fbDataChannel <- fbDat
		}

	} else {
		fbDat.printStr = "No metadata to update!"
		fbDataChannel <- fbDat
	}
}

func runRclone(rcbin, rcRemote, ksDir, rcConf string, err chan<- error) {
	syncCmd := exec.Command(rcbin, "sync", rcRemote, ksDir, "--config", rcConf)
	cmdErr := syncCmd.Run()
	err <- cmdErr
}

// syncBooks runs the rclone program using the preconfigered configuration file.
func syncBooks(rcBin, rcConf, rcRemote, ksDir, krcloneDir string, fbDataChannel chan fbinkData, fbBSerr chan error) {
	var fbDat fbinkData
	if !strings.HasSuffix(rcRemote, ":") {
		rcRemote += ":"
	}
	fbDat.printStr = "Starting sync... Please wait"
	fbDataChannel <- fbDat
	rcErr := make(chan error, 1)
	go runRclone(rcBin, rcRemote, ksDir, rcConf, rcErr)
	for len(rcErr) == 0 {
		time.Sleep(500 * time.Millisecond)
		fbDat.updateLastStr = true
		fbDat.printStr = "Waiting for rclone"
		fbDataChannel <- fbDat
	}
	fbDat.updateLastStr = false
	err := <-rcErr
	if err != nil {
		fbDat.printStr = err.Error()
		fbDataChannel <- fbDat
		return
	}
	fbDat.printStr = "Simulating USB... Please wait"
	fbDataChannel <- fbDat
	// Sync has succeeded. We need Nickel to process the new files, so we simulate
	// a USB connection. It turns out, 5 seconds may not be nearly long enough. Now
	// set to approx 60 sec
	nickelUSBplug()
	for i := 0; i < 120; i++ {
		fbDat.buttonScan = true
		fbDataChannel <- fbDat
		err := <-fbBSerr
		fbDat.buttonScan = false
		if i == 119 && err != nil {
			fbDat.printStr = err.Error()
			fbDataChannel <- fbDat
			logErrPrint(err)
			return
		}
		if err == nil {
			break
		}
		fbDat.updateLastStr = true
		fbDat.printStr = "Waiting for USB connect screen"
		fbDataChannel <- fbDat
		time.Sleep(500 * time.Millisecond)
	}
	fbDat.updateLastStr = false
	time.Sleep(5 * time.Second)
	nickelUSBunplug()
	fbDat.printStr = "Sync complete! Please rerun kobo-rclone to update metadata."
	fbDataChannel <- fbDat
	waitForMount(30)
	// Create the lock file to inform our program to get the metadata on next run
	f, _ := os.Create(filepath.Join(krcloneDir, metaLockFile))
	defer f.Close()
}

func main() {
	// Setup FBInk channels
	fbDataChan := make(chan fbinkData, 2)
	fbBtnScnChan := make(chan error)
	// Start the FBInk goroutine
	go fbInk(fbDataChan, fbBtnScnChan)
	var fbDat fbinkData
	// Discover what directory we are running from
	krcloneDir, err := os.Executable()
	log.Printf(krcloneDir)
	if err != nil {
		fbDat.printStr = err.Error()
		fbDataChan <- fbDat
		log.Fatal(err)
	}
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
		fbDat.printStr = err.Error()
		fbDataChan <- fbDat
		log.Fatal(err)
	}

	// Run kobo-rclone with our configured settings
	rcloneBin := filepath.Join(krcloneDir, "rclone")
	rcloneConfig := filepath.Join(krcloneDir, krCfg.RcloneCfg)
	bookDir := filepath.Join(onboardMnt, krCfg.KRbookDir)
	if metadataLockfileExists(krcloneDir) {
		updateMetadata(bookDir, krcloneDir, fbDataChan, fbBtnScnChan)

	} else {
		syncBooks(rcloneBin, rcloneConfig, krCfg.RCremoteName, bookDir, krcloneDir, fbDataChan, fbBtnScnChan)
	}
}
