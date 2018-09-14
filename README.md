# kobo-rclone
A WIP rclone wrapper for Kobo ereaders

## What is rclone?
In their own words, rclone is "rsync for cloud storage". It is available from https://rclone.org/

It is a CLI program to sync files from a large number of different 'cloud' backends.

## And kobo-rclone?
kobo-rclone is both a wrapper for the rclone binary to sync ebooks onto a Kobo ereader wirelessly, and a metadata parser/updater, when used in conjunction with Calibre's "Connect to folder" feature. More specifically, kobo-rclone can currently add series information to book entries after a sync.

## Changelog
**pre-0.3.0**
* kobo-rclone now uses a config file to store some settings. It should be called krclone-cfg.toml and placed in the same directory as the krclone binary. An example file is provided in this repository.
* FBInk handling has changed. Along with that is a new go-fbink-v2 wrapper, which will need to be installed to compile this new version of kobo-rclone
* Saner error handling.

**0.2.0**
* Now integrates FBInk as a static library in the binary. No need to download it separately now. Instead, a wrapper called go-fbink has been created.
* Uses the newly created `fbink_button_scan()` function to detect the Nickel USB connect screen. Additionally, it also handles pressing the button automatically as well. Dumping your own touch event is no longer necessary.
* Safety checks added to ensure filesystem mounts/unmounts occur as they should.

**0.1.0**
* Initial release

## Installing
kobo-rclone is NOT yet suitable for everyday use, therefore no binaries are available at this point. Installing from source is as follows:

### Obtain Go
Install the Go distribution for your platform. See https://golang.org/doc/install for documentation.

### Install dependencies
Some dependencies of kobo-rclone require the use of CGO. Therefore, an ARM GCC cross compiler is necessary.

I highly recommed building kobo-rclone on Linux using the Koreader toolchain found at `https://github.com/koreader/koxtoolchain`. A fair warning that this could take over 40 minutes to install however, although it is entirely automated.

If building in Windows, `gcc-linaro-arm-linux-gnueabihf-4.8-2013.04-20130417` has been used successfully. Simply extract it to a directory of your choosing.

### Obtain kobo-rclone
kobo-rclone can be downloaded using `go get`
```
go get github.com/shermp/kobo-rclone/krclone
```
This should install all necessary dependencies.

### Building Binary
The following environment variables need to be set to compile kobo-rclone
```
GOOS=linux
GOARCH=arm
CGO_ENABLED=1
# The following are example paths. Replace with the paths to your installed cross compiler
CC=path/to/gcc
CXX=path/to/g++
```
A shell script or batch file may be helpful to set these for a terminal session.

To build, run the following command, once the above environment variables have been set.
```
go build go/src/github.com/shermp/kobo-rclone/krclone
```
A binary called `krclone` will be copied to the current directory.

Note that fbink is now included as a static library, and should now be included in the main binary after this step.

You may also wish to strip the binary using `path/to/toolchain/toolchain-strip krclone`, where toolchain is the name of your chosen cross compiler. The stripped binary will be approx 50%-60% the size of the original binary.

### Obtaining rclone
Rclone is available as an ARM binary. Visit the download page https://rclone.org/downloads/ and download the Linux, ARM 32-bit distribution from the table.

### Installing on Kobo
Currently, telnet or SSH access to a Kobo device is almost mandatory.

On the main memory of your Kobo (`/mnt/onboard`), create the directory `.adds/kobo-rclone`

Copy the `rclone` and `krclone` binaries to the `kobo-rclone` directory on the Kobo. Also copy the included `krclone-cfg.toml` to this directory.

Rclone require a configuration file. By default, kobo-rclone is configured to use `rclone.conf` in the `kobo-rclone` directory. This file may be generated on the Kobo, or your development PC. To generate on the Kobo:
```
# cd /mnt/onboard/.adds/kobo-rclone
# ./rclone config --config "./rclone.conf"
```
follow the interactive prompts for your cloud provider. I have currently tested with SFTP. Note, by default kobo-rclone is configured to use the root of whatever 'cloud' directory you set up.

To alter some default configurations, feel free to modify the `krclone-cfg.toml`, according to the instructions in the file.

## Runing kobo-rclone
Running kobo-rclone is currently a simple affair. From telnet/SSH:
```
# cd /mnt/onboard/.adds/kobo-rclone
# ./krclone

// wait for sync, then for Nickel to process files, if any
// run again to process any metadata, such as updating series info.
```
Synced books are stored in `/mnt/onboard/krclone-books` by default. You may change this in the config file.

It is higly recommended to use Calibre's "Connect to folder" option to "connect" to your sync directory on your PC. This transferrs the `.metadata.calibre` file used by kobo-rclone to populate the series entry in the Kobo DB. It is also recommended to disable unsupported filetypes in the "connect to folder" settings.

## Future plans
Once this project has had further testing, bug fixing, and improvements, a binary release will be made available to simplify deployment. It will then be integrated with `Kute File Monitor` to enable using it without telnet/SSH

## Disclaimers
This project is very much still a WORK IN PROGRESS. It could corrupt your Kobo database. It could corrupt your books/database partition. Be prepared to perform a factory reset if and when things go wrong.

Use this software AT YOUR OWN RISK.

## Further information.
This project includes fbink by @NiLuJe, which is a tool to print stuff on eink screens.

fbink is licensed under the AGPL3 license. It can be found at the following github repository:
https://github.com/NiLuJe/FBInk
