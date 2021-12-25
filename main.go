package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

const (
	ZK_VERSION                         = "0.0.0"
	DEFAULT_SERVER_PORT                = 8080
	DEFAULT_PORT_HELP_TEXT             = "The port to which the server will bind"
	DEFAULT_SERVER_DIRECTORY           = "."
	DEFAULT_SERVER_DIRECTORY_HELP_TEXT = "The directory that will be served"
	ZK_ROOT_FILENAME                   = ".zk-root"
)

func PrintHelpText() {
	fmt.Println("ZK is a command line tool for managing zettelkastens")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("\tzk <command> [arguments]")
	fmt.Println("Available commands:")
	fmt.Println("\tzk version\tGet the current ZK CLI version")
	fmt.Println("\tzk serve\tServe a ZK in the current working directory")
	fmt.Println("\tzk mv frompath topath\tMove the item(s) at frompath to topath, updating references to them")
}

func IsRootDir(path string) bool {
	if runtime.GOOS == "windows" {
		return path == fmt.Sprintf("%s\\", filepath.VolumeName(path))
	}
	return path == "/"
}

// Accepts a desired directory to be served. Returns
// the nearest superdirectory that has a .zk-root file,
// returns an error if a .zk-root cannot be found in the desired
// directory or any superdirectories
func ZkRoot(desiredPath string) (string, error) {
	zkPath := filepath.Join(desiredPath, ZK_ROOT_FILENAME)
	if _, err := os.Stat(zkPath); errors.Is(err, os.ErrNotExist) {
		// .zk-root not found
		if IsRootDir(desiredPath) {
			// This is the root directory so we can't go further. Return an error
			return "", errors.New(".zk-root cannot be found")
		}
		lastSeparatorIndex := strings.LastIndex(desiredPath, string(os.PathSeparator))
		if lastSeparatorIndex == -1 {
			// The root directory check above didn't work as intended.
			// This is another "failsafe" base case for the error path
			return "", errors.New(".zk-root cannot be found")
		}
		return ZkRoot(desiredPath[:lastSeparatorIndex])
	}
	return desiredPath, nil
}

func main() {
	// The port on localhost to which the server will bind
	// on localhost, when the "serve" subcommand or its alias are executed
	var serverPort int
	// The directory that will be served. It or an ancestor directory must have a .zk-root file
	var serverDesiredDirectory string
	serveFlagSet := flag.NewFlagSet("serve", flag.ExitOnError)
	serveFlagSet.IntVar(&serverPort, "port", DEFAULT_SERVER_PORT, DEFAULT_PORT_HELP_TEXT)
	serveFlagSet.IntVar(&serverPort, "p", DEFAULT_SERVER_PORT, DEFAULT_PORT_HELP_TEXT)
	serveFlagSet.StringVar(&serverDesiredDirectory, "dir", DEFAULT_SERVER_DIRECTORY, DEFAULT_SERVER_DIRECTORY_HELP_TEXT)
	serveFlagSet.StringVar(&serverDesiredDirectory, "d", DEFAULT_SERVER_DIRECTORY, DEFAULT_SERVER_DIRECTORY_HELP_TEXT)

	if len(os.Args) < 2 {
		PrintHelpText()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "version":
		fmt.Printf("ZK Version: %s\n", ZK_VERSION)
	case "serve", "run":
		// The serve subcommand serves a given ZK project
		// to localhost at a given port. It enables the project
		// to be read / consumed from the browser
		var serverDirectory string
		var zkRootDirectory string
		serveFlagSet.Parse(os.Args[2:])
		workingDir, err := os.Getwd()
		if err != nil {
			fmt.Printf("Error getting current working directory: %s\n", err.Error())
			os.Exit(1)
		}
		serverDirectory = filepath.Join(workingDir, serverDesiredDirectory)
		zkRootDirectory, err = ZkRoot(serverDirectory)
		if err != nil {
			fmt.Printf("Error finding ZK root: %s\n", err.Error())
			os.Exit(1)
		}

		fs := http.FileServer(http.Dir(zkRootDirectory))
		http.Handle("/", fs)

		fmt.Printf("Listening on http://localhost:%d\n", serverPort)
		err = http.ListenAndServe(fmt.Sprintf(":%d", serverPort), nil)
		if err != nil {
			fmt.Printf("Error starting HTTP server: %s\n", err.Error())
			os.Exit(1)
		}
	case "mv", "move":
		if len(os.Args) < 4 {
			fmt.Println("zk mv usage:")
			fmt.Println("\tzk mv frompath topath\tMove the item(s) at frompath to topath, updating references to them")
			os.Exit(1)
		}
		fromPath := os.Args[2]
		toPath := os.Args[3]
		workingDir, err := os.Getwd()
		if err != nil {
			fmt.Printf("Error getting current working directory: %s\n", err.Error())
			os.Exit(1)
		}
		zkRootDirectory, err := ZkRoot(workingDir)
		if err != nil {
			fmt.Printf("Error finding ZK root: %s\n", err.Error())
			os.Exit(1)
		}
		absFromPath := filepath.Join(workingDir, fromPath)
		absToPath := filepath.Join(workingDir, toPath)
		zkRelativeFromPath := absFromPath[len(zkRootDirectory):]
		zkRelativeToPath := absToPath[len(zkRootDirectory):]
		zkRelativeToPath = strings.TrimSuffix(zkRelativeToPath, "/index.html")
		_, err = os.Stat(absFromPath)
		if errors.Is(err, os.ErrNotExist) {
			fmt.Printf("File %s does not exist\n", absFromPath)
			os.Exit(1)
		}
		filepath.WalkDir(zkRootDirectory, func(path string, dirEntry fs.DirEntry, err error) error {
			// If this is an HTML file, we can work with it
			if strings.HasSuffix(dirEntry.Name(), ".html") {
				fileContent, err := os.Open(path) // the file is inside the local directory
				if err != nil {
					return err
				}
				defer fileContent.Close()
				html, err := goquery.NewDocumentFromReader(fileContent)
				if err != nil {
					return err
				}
				foundReference := false
				html.Find("a").Each(func(index int, elem *goquery.Selection) {
					href, exists := elem.Attr("href")
					hrefMatchesDirectoryIndex := (filepath.Base(zkRelativeFromPath) == "index.html" && href == filepath.Dir(zkRelativeFromPath))
					hrefMatchesPageWithoutExtension := strings.HasSuffix(zkRelativeFromPath, ".html") && href == zkRelativeFromPath[:len(zkRelativeFromPath)-5]
					if exists && (href == zkRelativeFromPath || hrefMatchesDirectoryIndex || hrefMatchesPageWithoutExtension) {
						foundReference = true
						elem.SetAttr("href", zkRelativeToPath)
					}
				})
				if foundReference {
					// Only edit a file if it references our moving file
					newHtmlString, err := html.Html()
					if err != nil {
						return err
					}
					newHtmlBytes := []byte(newHtmlString)
					ioutil.WriteFile(path, newHtmlBytes, 0777)
				}
			}
			return nil
		})
		err = os.Rename(absFromPath, absToPath)
		if err != nil {
			fmt.Printf("Error moving item in file tree: %s\n", err.Error())
			os.Exit(1)
		}
	default:
		// In the future, run zk-x command, return 1 on failure
		PrintHelpText()
		os.Exit(2)
	}
}
