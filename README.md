# kobo-rclone
A WIP rclone wrapper for Kobo ereaders

## What is rclone?
In their own words, rclone is "rsync for cloud storage". It is available from https://rclone.org/

It is a CLI program to sync files from a large number of different 'cloud' backends.

## And kobo-rclone?
kobo-rclone is both a wrapper for the rclone binary to sync ebooks onto a Kobo ereader wirelessly, and a metadata parser/updater, when used in conjunction with Calibre's "Connect to folder" feature. More specifically, kobo-rclone can currently add series information to book entries after a sync.

## Installing
kobo-rclone is NOT yet suitable for everyday use, therefore no binaries are available at this point. Installing from source is as follows:

### Obtain Go
Install the Go distribution for your platform. See https://golang.org/doc/install for documentation.

### Install dependencies
kobo-rclone uses the `go-sqlite3` package to interface with the Kobo DB. Unfortunately, this require `CGO` support. First, install `go-sqlite3` with
```
go get github.com/mattn/go-sqlite3
```
You will need an ARM GCC cross compiler available to build kobo-rclone correctly with `go-sqlite3`. `gcc-linaro-arm-linux-gnueabihf-4.8-2013.04-20130417` has been used successfully. Extract it to a directory of your choosing, and set the `CC` and `CXX` environment variables to `path/to/gcc` and `path/to/g++` respectively.

### Obtain kobo-rclone
kobo-rclone can be downloaded using `go get`
```
go get https://github.com/shermp/kobo-rclone
```

### Building Binary
The following environment variables need to be set to compile kobo-rclone
```
GOOS=linux
GOARCH=arm
CGO_ENABLED=1
CC=path/to/gcc
CXX=path/to/g++
```
A script or batch file may be helpful to set these for a terminal session.

To build, run the following command, once the above environment variables have been set.
```
go build path/to/kobo-rclone/krclone.go
```
A binary called `krclone` will be copied to the current directory.

### Obtaining rclone
Rclone is available as an ARM binary. Visit the download page https://rclone.org/downloads/ and download the Linux, ARM 32-bit distribution from the table.

### Installing on Kobo
Currently, telnet or SSH access to a Kobo device is almost mandatory.

On the main memory of your Kobo (`/mnt/onboard`), create the directory `.adds/kobo-rclone`

In the `kobo-rclone` directory, create the subdirectory `touchevents/usbconnect`

Obtain a dump of the touch event required to enable the USB connection. I did the following (for my Aura H2O, model number N250):
```
# cd /
# usb plug add >> /tmp/nickel-hardware-status
# cat /dev/input/event1 > /N250

// tap the connect button on-screen, then Ctrl-C to exit 'cat'

# usb plug remove >> /tmp/nickel-hardware-status
# mv /N250 /mnt/onboard/.adds/kobo-rclone/touchevents/usbconnect/N250
```
Note that the filename of the above dump must match the model number of the Kobo you are using.

Copy the `rclone` and `krclone` binaries to the `kobo-rclone` directory on the Kobo.

Rclone require a configuration file. kobo-rclone is configured to use `rclone.conf` in the `kobo-rclone` directory. This file may be generated on the Kobo, or your development PC. To generate on the Kobo:
```
# cd /mnt/onboard/.adds/kobo-rclone
# ./rclone config --config "./rclone.conf"
```
follow the interactive prompts for your cloud provider. I have currently tested with SFTP. Note kobo-rclone is currently configured to use the root of whatever 'cloud' directory you set up. This could be made configurable later.

## Runing kobo-rclone
Running kobo-rclone is currently a simple affair. From telnet/SSH:
```
# cd /mnt/onboard/.adds/kobo-rclone
# ./krclone

// wait for sync, then for Nickel to process files, if any
// run again to process any metadata, such as updating series info.
```
Synced books are stored in `/mnt/onboard/krclone-books`

It is higly recommended to use Calibre's "Connect to folder" option to "connect" to your sync directory on your PC. This transferrs the `.metadata.calibre` file used by kobo-rclone to populate the series entry in the Kobo DB. It is also recommended to disable unsupported filetypes in the "connect to folder" settings.

## Future plans
Once this project has had further testing, bug fixing, and improvements, a binary release will be made available to simplify deployment. It will then be integrated with `Kute File Monitor` to enable using it without telnet/SSH

## Disclaimers
This project is very much still a WORK IN PROGRESS. It could corrupt your Kobo database. It could corrupt your books/database partition. Be prepared to perform a factory reset if and when things go wrong.

Use this software AT YOUR OWN RISK.