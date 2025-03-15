package main

import (
	"bufio"
	"enex2paperless/internal/config"
	"enex2paperless/internal/logging"
	"enex2paperless/pkg/enex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"
)

func main() {
	// define root command
	rootCmd := &cobra.Command{
		Use:   "enex2paperless [file path]",
		Short: "ENEX to Paperless-NGX parser",
		Long:  `An ENEX file parser for Paperless-NGX. https://github.com/kevinzehnder/enex2paperless`,
		Args:  cobra.MinimumNArgs(1),
		PreRun: func(cmd *cobra.Command, args []string) {
			// this block will execute after flag parsing and before the main Run

			// configure SLOG with the determined log level from verbose flag
			verbose, err := cmd.Flags().GetBool("verbose") // Ensure to get the flag value correctly
			if err != nil {
				fmt.Println("Error retrieving verbose flag:", err)
				os.Exit(1)
			}

			// set log level
			var logLevel slog.Level
			if verbose {
				logLevel = slog.LevelDebug
			} else {
				logLevel = slog.LevelInfo
			}

			// nocolor option
			nocolor, err := cmd.Flags().GetBool("nocolor")
			if err != nil {
				fmt.Println("Error retrieving nocolor flag:", err)
				os.Exit(1)
			}

			opts := &slog.HandlerOptions{
				Level: logLevel,
			}

			// use custom slog Handler
			logger := slog.New(logging.NewHandler(opts, nocolor, verbose))
			slog.SetDefault(logger)

			// handle configuration
			settings, err := config.GetConfig()
			if err != nil {
				slog.Error("configuration error:", "error", err)
				os.Exit(1)
			}
			slog.Debug(fmt.Sprintf("configuration: %v", settings))

			// add to configuration
			outputfolder, err := cmd.Flags().GetString("outputfolder")
			if err != nil {
				fmt.Println("Error retrieving outputfolder flag:", err)
				os.Exit(1)
			}

			if outputfolder != "" {
				config.SetOutputFolder(outputfolder)
			}

			// Set additional tags if provided
			tags, err := cmd.Flags().GetStringSlice("tags")
			if err != nil {
				fmt.Println("Error retrieving tag flag:", err)
				os.Exit(1)
			}

			// Get current settings to check UseFilenameAsTag
			currentSettings, _ := config.GetConfig()

			useFilenameAsTag, err := cmd.Flags().GetBool("use-filename-tag")
			if err != nil {
				fmt.Println("Error retrieving tag flag:", err)
				os.Exit(1)
			}
			// Only override config if explicitly set on command line
			if cmd.Flags().Changed("use-filename-tag") {
				config.SetUseFilenameAsTag(useFilenameAsTag)
			}

			// Check both the flag and the config setting
			if useFilenameAsTag || currentSettings.UseFilenameAsTag {
				// Extract filename without path and extension
				baseName := filepath.Base(args[0])
				tagName := strings.TrimSuffix(baseName, filepath.Ext(baseName))
				tags = append(tags, tagName)
			}

			if len(tags) > 0 {
				config.SetAdditionalTags(tags)
			}

			// set unzip option
			unzip, err := cmd.Flags().GetBool("unzip")
			if err != nil {
				fmt.Println("Error retrieving unzip flag:", err)
				os.Exit(1)
			}
			// Only override config if explicitly set on command line
			if cmd.Flags().Changed("unzip") {
				config.SetUnzip(unzip)
			}

			// set link field ID
			linkFieldID, err := cmd.Flags().GetInt("link")
			if err != nil {
				fmt.Println("Error retrieving link flag:", err)
				os.Exit(1)
			}
			// Only override config if explicitly set on command line
			if cmd.Flags().Changed("link") || linkFieldID > 0 {
				config.SetLinkFieldID(linkFieldID)
			}

			// set title prefix option
			titlePrefix, err := cmd.Flags().GetBool("titleprefix")
			if err != nil {
				fmt.Println("Error retrieving titleprefix flag:", err)
				os.Exit(1)
			}
			// Only override config if explicitly set on command line
			if cmd.Flags().Changed("titleprefix") {
				config.SetTitlePrefix(titlePrefix)
			}

			// set markdown conversion option
			convertMarkdown, err := cmd.Flags().GetBool("convert-markdown")
			if err != nil {
				fmt.Println("Error retrieving convert-markdown flag:", err)
				os.Exit(1)
			}
			// Only override config if explicitly set on command line
			if cmd.Flags().Changed("convert-markdown") {
				config.SetConvertMarkdown(convertMarkdown)
			}

			// set Apple iWork to PDF conversion option
			convertAppleToPDF, err := cmd.Flags().GetBool("convert-apple-pdf")
			if err != nil {
				fmt.Println("Error retrieving convert-apple-pdf flag:", err)
				os.Exit(1)
			}
			// Only override config if explicitly set on command line
			if cmd.Flags().Changed("convert-apple-pdf") {
				config.SetConvertAppleToPDF(convertAppleToPDF)
			}

			// set concurrent workers
			concurrent, err := cmd.Flags().GetInt("concurrent")
			if err != nil {
				fmt.Println("Error retrieving concurrent flag:", err)
				os.Exit(1)
			}
			// Only override config if explicitly set on command line
			if cmd.Flags().Changed("concurrent") && concurrent > 0 {
				config.SetConcurrentWorkers(concurrent)
			}

			// set correspondent tag prefix
			correspondentTagPrefix, err := cmd.Flags().GetString("correspondent-tag-prefix")
			if err != nil {
				fmt.Println("Error retrieving correspondent-tag-prefix flag:", err)
				os.Exit(1)
			}
			// Only override config if explicitly set on command line
			if cmd.Flags().Changed("correspondent-tag-prefix") {
				config.SetCorrespondentTagPrefix(correspondentTagPrefix)
			}

			// set noname folder
			noname, err := cmd.Flags().GetString("noname")
			if err != nil {
				fmt.Println("Error retrieving noname flag:", err)
				os.Exit(1)
			}
			// Only override config if explicitly set on command line
			if cmd.Flags().Changed("noname") {
				config.SetNoNameFolder(noname)
			}

			// set excluded output folder
			excludedOutput, err := cmd.Flags().GetString("excluded-outputfolder")
			if err != nil {
				fmt.Println("Error retrieving excluded-outputfolder flag:", err)
				os.Exit(1)
			}
			// Only override config if explicitly set on command line
			if cmd.Flags().Changed("excluded-outputfolder") {
				config.SetExcludedOutputFolder(excludedOutput)
			}
		},

		// run main function
		Run: importENEX,
	}

	// add flags
	var howMany int
	rootCmd.PersistentFlags().IntVarP(&howMany, "concurrent", "c", 1, "Number of concurrent consumers")

	var verbose bool
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose logging")

	var nocolor bool
	rootCmd.PersistentFlags().BoolVarP(&nocolor, "nocolor", "n", false, "Disable colored output")

	var outputfolder string
	rootCmd.PersistentFlags().StringVarP(&outputfolder, "outputfolder", "o", "", "Output attachements to this folder, NOT paperless.")

	rootCmd.PersistentFlags().StringSliceP("tags", "t", nil, "Additional tags to add to all documents.")

	var useFilenameAsTag bool
	rootCmd.PersistentFlags().BoolVarP(&useFilenameAsTag, "use-filename-tag", "T", false, "Add the ENEX filename as tag to all documents.")

	var unzip bool
	rootCmd.PersistentFlags().BoolVarP(&unzip, "unzip", "u", false, "Unzip .zip files found in notes")

	var linkFieldID int
	rootCmd.PersistentFlags().IntVarP(&linkFieldID, "link", "l", 0, "Custom field ID for document linking")

	var titlePrefix bool
	rootCmd.PersistentFlags().BoolVar(&titlePrefix, "titleprefix", false, "Prefix filenames with note titles when using outputfolder")

	var convertMarkdown bool
	rootCmd.PersistentFlags().BoolVarP(&convertMarkdown, "convert-markdown", "m", false, "Convert note content to markdown")

	var convertAppleToPDF bool
	rootCmd.PersistentFlags().BoolVar(&convertAppleToPDF, "convert-apple-pdf", false, "Convert Apple iWork files to PDF")

	var correspondentTagPrefix string
	rootCmd.PersistentFlags().StringVar(&correspondentTagPrefix, "correspondent-tag-prefix", "", "Prefix for tags to be used as correspondents (e.g. '@')")

	var nonameFolder string
	rootCmd.PersistentFlags().StringVar(&nonameFolder, "noname", "", "Folder for saving files with no original filename")

	var excludedOutputFolder string
	rootCmd.PersistentFlags().StringVarP(&excludedOutputFolder, "excluded-outputfolder", "e", "", "Folder for saving excluded files that don't match allowed file types")

	// run root command
	err := rootCmd.Execute()
	if err != nil {
		fmt.Println("Error executing command:", err)
		os.Exit(1)
	}
}

func importENEX(cmd *cobra.Command, args []string) {
	// Register main process with ID 0
	logging.SetWorkerLogger(0)

	slog.Debug("starting importENEX")
	settings, _ := config.GetConfig()

	if settings.OutputFolder != "" {
		slog.Info(fmt.Sprintf("Output to local storage is enabled. Target is: %v", settings.OutputFolder))
	}

	if settings.ExcludedOutputFolder != "" {
		slog.Info(fmt.Sprintf("Excluded files output folder is enabled. Target is: %v", settings.ExcludedOutputFolder))
	}

	// Check if we need to add the filename as a tag
	if settings.UseFilenameAsTag {
		// Extract filename without path and extension
		baseName := filepath.Base(args[0])
		tagName := strings.TrimSuffix(baseName, filepath.Ext(baseName))

		// Add to additional tags if not already added by the command-line flag
		if !contains(settings.AdditionalTags, tagName) {
			newTags := append(settings.AdditionalTags, tagName)
			config.SetAdditionalTags(newTags)
		}
	}

	// determine how many concurrent uploaders we want
	howMany := settings.ConcurrentWorkers
	if howMany <= 0 {
		howMany = 1 // Default to 1 if not set in config
	}

	// prepare input file
	filePath := args[0]
	inputFile := enex.NewEnexFile()

	// prepare channels
	noteChannel := make(chan enex.Note)
	failedNoteChannel := make(chan enex.Note)
	failedNoteSignal := make(chan bool)

	// Failure Catcher
	var failedNotes []enex.Note
	go func() {
		// Register failed note catcher4
		logging.SetWorkerLogger(0)
		slog.Debug("Starting failed note catcher")
		enex.FailedNoteCatcher(failedNoteChannel, &failedNotes)
		slog.Debug("Failed note catcher finished")
		failedNoteSignal <- true
	}()

	// Producer
	go func() {
		// Register producer1
		logging.SetWorkerLogger(0)
		slog.Info("Starting ENEX file processing", "file", filePath)
		err := inputFile.ReadFromFile(filePath, noteChannel)
		if err != nil {
			slog.Error("failed to read from file", "error", err)
			os.Exit(1)
		}
		slog.Info("Finished reading ENEX file")
	}()

	// Consumers
	var wg sync.WaitGroup
	wg.Add(howMany)

	for i := 0; i < howMany; i++ {
		workerID := i + 1 // Using 1-based worker IDs for readability

		go func(id int) {
			// Register worker with its ID
			logging.SetWorkerLogger(id)
			slog.Info("Starting worker")

			err := inputFile.UploadFromNoteChannel(noteChannel, failedNoteChannel, settings.OutputFolder)
			if err != nil {
				slog.Error("failed to upload resources", "error", err)
				os.Exit(1)
			}

			slog.Debug("Worker finished")
			wg.Done()
		}(workerID)
	}
	slog.Debug("waiting for Consumers (WaitGroup)")
	wg.Wait()

	// close failedNoteChannel when consumers are done
	close(failedNoteChannel)

	// wait for FailedNoteCatcher
	slog.Debug("waiting for FailedNoteCatcher")
	<-failedNoteSignal

	// log results
	slog.Info("ENEX processing done",
		slog.Int("numberOfNotes", int(inputFile.NumNotes.Load())),
		slog.Int("totalFiles", int(inputFile.Uploads.Load())),
	)

	for {
		// if we still have failedNotes in this iteration, keep going
		if len(failedNotes) == 0 {
			break
		}

		slog.Warn("there have been errors, starting retry cycle", "errors", len(failedNotes))
		PressKeyToContinue()

		// all failed notes are now in failedNotes slice
		// push notes that failed this Cycle into failedThisCycle slice
		failedThisCycle := []enex.Note{}

		// reset failedNoteChannel
		failedNoteChannel = make(chan enex.Note)

		// this feeds the failedNotes slice into the failedNoteChannel
		go func() {
			// Register retry failed note catcher3
			logging.SetWorkerLogger(0)
			slog.Debug("Starting retry failed note catcher")
			enex.FailedNoteCatcher(failedNoteChannel, &failedThisCycle)
			slog.Debug("Retry failed note catcher finished")
			failedNoteSignal <- true
		}()

		// this feeds the failedNotes into the Retry Channel
		retryChannel := make(chan enex.Note)
		go func() {
			// Register retry feeder2
			logging.SetWorkerLogger(0)
			slog.Debug("Starting retry feeder")
			enex.RetryFeeder(&failedNotes, retryChannel)
			slog.Debug("Retry feeder finished")
		}()

		// this works on the retry channel
		wg.Add(1)
		go func() {
			// Register retry worker with ID 999
			logging.SetWorkerLogger(999)
			slog.Info("Starting retry worker")
			err := inputFile.UploadFromNoteChannel(retryChannel, failedNoteChannel, settings.OutputFolder)
			if err != nil {
				slog.Error("failed to upload resources", "error", err)
				os.Exit(1)
			}

			slog.Debug("Retry worker finished")
			wg.Done()
		}()
		wg.Wait()

		// when the uploader is done, we can close the failedNoteChannel
		// to signal to the FailedNote Catcher that it can stop
		close(failedNoteChannel)

		// then we wait for the FailedNoteCatcher to stop
		<-failedNoteSignal

		// we move the notes that failed this cycle into the failedNotes variable
		failedNotes = failedThisCycle
	}

	slog.Info("all notes processed successfully")
}

func PressKeyToContinue() {
	fmt.Println("Press 'x' to exit or any other key to continue.")
	for {
		key := getUserInput()
		if key == "x" {
			fmt.Println("Exiting...")
			os.Exit(1)
		} else {
			return
		}
	}
}

func getUserInput() string {
	reader := bufio.NewReader(os.Stdin)
	char, _, err := reader.ReadRune()
	if err != nil {
		fmt.Println("Error reading input:", err)
		return ""
	}

	return string(char)
}

func contains(slice []string, item string) bool {
	for _, i := range slice {
		if i == item {
			return true
		}
	}
	return false
}
