package main

import (
	"bufio"
	"enex2paperless/internal/config"
	"enex2paperless/internal/logging"
	"enex2paperless/pkg/enex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

func main() {
	// define root command
	rootCmd := &cobra.Command{
		Use:   "enex2paperless [file path]",
		Short: "ENEX to Paperless-NGX parser",
		Long:  `An ENEX file parser for Paperless-NGX. https://github.com/kevinzehnder/enex2paperless`,
		Args: func(cmd *cobra.Command, args []string) error {
			// Allow running without arguments when using process-tasks flag
			processTasksOnly, _ := cmd.Flags().GetBool("process-tasks")
			if processTasksOnly {
				return nil
			}

			// Allow running without arguments when using directory flag
			dirMode, _ := cmd.Flags().GetBool("dir")
			if dirMode && len(args) == 1 {
				return nil
			}

			// Otherwise require at least one argument (the ENEX file or directory path)
			if len(args) < 1 {
				return fmt.Errorf("requires at least one argument (ENEX file path or directory path)")
			}
			return nil
		},
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

			// Check if we're only processing tasks
			processTasksOnly, _ := cmd.Flags().GetBool("process-tasks")
			if processTasksOnly && len(args) == 0 {
				// Skip argument processing when in process-tasks-only mode with no arguments
				return
			}

			// Set additional tags if provided
			tags, err := cmd.Flags().GetStringSlice("tags")
			if err != nil {
				fmt.Println("Error retrieving tag flag:", err)
				os.Exit(1)
			}

			useFilenameAsTag, err := cmd.Flags().GetBool("use-filename-tag")
			if err != nil {
				fmt.Println("Error retrieving tag flag:", err)
				os.Exit(1)
			}
			// Only override config if explicitly set on command line
			if cmd.Flags().Changed("use-filename-tag") {
				config.SetUseFilenameAsTag(useFilenameAsTag)
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

			// The process-tasks flag is already defined at the root level, no need to define it again here
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

	var processTasksOnly bool
	rootCmd.PersistentFlags().BoolVar(&processTasksOnly, "process-tasks", false, "Only process pending tasks and link documents (skip file processing)")

	var dirMode bool
	rootCmd.PersistentFlags().BoolVarP(&dirMode, "dir", "d", false, "process all ENEX files in the specified directory without prompting for confirmation")

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

	// Check if we should only process tasks
	processTasksOnly, _ := cmd.Flags().GetBool("process-tasks")
	if processTasksOnly {
		slog.Info("Running in process-tasks-only mode")

		// Create EnexFile with task tracker
		inputFile := enex.NewEnexFile()

		// Ensure HTTP client is initialized
		if inputFile.Client() == nil {
			inputFile.SetClient(&http.Client{
				Timeout: time.Second * 30, // Use a longer timeout for task processing
			})
			slog.Debug("HTTP client initialized")
		}

		// Process pending tasks
		slog.Info("Processing pending tasks and linking documents")
		if err := inputFile.ProcessPendingTasks(); err != nil {
			slog.Error("failed to process pending tasks and link documents", "error", err)
			os.Exit(1)
		}

		slog.Info("Task processing completed successfully")
		return
	}

	// Regular ENEX processing mode
	if settings.OutputFolder != "" {
		slog.Info(fmt.Sprintf("Output to local storage is enabled. Target is: %v", settings.OutputFolder))
	}

	// Check if we're in directory mode
	dirMode, _ := cmd.Flags().GetBool("dir")

	// If not in directory mode, check if the path is a directory and prompt the user
	if !dirMode && len(args) > 0 {
		dirPath := args[0]
		fileInfo, err := os.Stat(dirPath)

		// If the path exists and is a directory
		if err == nil && fileInfo.IsDir() {
			// Find all ENEX files in the directory
			enexFiles, err := findEnexFiles(dirPath)
			if err == nil && len(enexFiles) > 0 {
				fmt.Printf("Found %d ENEX files in directory '%s'.\n", len(enexFiles), dirPath)
				fmt.Print("Do you want to process all ENEX files in this directory? (y/n): ")

				response := getUserInput()
				if strings.ToLower(response) == "y" {
					// User wants to process all files, set dirMode to true
					dirMode = true
				} else {
					// User declined to process the directory
					fmt.Println("Directory processing declined. Please specify a single ENEX file instead.")
					os.Exit(0)
				}
			}
		}
	}

	if dirMode {
		if len(args) == 0 {
			slog.Error("No directory specified")
			os.Exit(1)
		}

		dirPath := args[0]

		// Check if the path is a directory
		fileInfo, err := os.Stat(dirPath)
		if err != nil {
			slog.Error("Error accessing directory", "error", err)
			os.Exit(1)
		}

		if !fileInfo.IsDir() {
			slog.Error("Specified path is not a directory", "path", dirPath)
			os.Exit(1)
		}

		// Find all ENEX files in the directory
		enexFiles, err := findEnexFiles(dirPath)
		if err != nil {
			slog.Error("Error finding ENEX files", "error", err)
			os.Exit(1)
		}

		if len(enexFiles) == 0 {
			slog.Error("No ENEX files found in directory", "directory", dirPath)
			os.Exit(1)
		}

		slog.Info("Found ENEX files in directory", "count", len(enexFiles), "directory", dirPath)

		// Create a single EnexFile instance for all processing
		inputFile := enex.NewEnexFile()

		// Count total notes across all files first
		var totalNotesAcrossFiles int
		slog.Info("Counting notes in all ENEX files...")
		for _, filePath := range enexFiles {
			fileNotes, err := inputFile.CountNotes(filePath)
			if err != nil {
				slog.Error("failed to count notes in file", "error", err, "file", filePath)
				continue
			}
			totalNotesAcrossFiles += fileNotes
		}
		slog.Info("Total notes across all files", "count", totalNotesAcrossFiles)

		// Store the total in the atomic counter
		inputFile.TotalNotesAll.Store(uint32(totalNotesAcrossFiles))
		slog.Info("Total notes across all enex files", "count", inputFile.TotalNotesAll.Load())

		// Reset the overall counter before processing
		inputFile.CurrentNoteAll.Store(0)

		// Collect all failed notes from all files
		var allFailedNotes []enex.Note

		// Process each file sequentially
		for _, filePath := range enexFiles {
			slog.Info(":::::::::::::::::: Processing ENEX file ::::::::::::::::::", "file", filePath)

			// Check if we need to add the filename as a tag
			if settings.UseFilenameAsTag {
				// Extract filename without path and extension
				baseName := filepath.Base(filePath)
				tagName := strings.TrimSuffix(baseName, filepath.Ext(baseName))

				// Add to additional tags if not already added by the command-line flag
				if !contains(settings.AdditionalTags, tagName) {
					newTags := append(settings.AdditionalTags, tagName)
					config.SetAdditionalTags(newTags)
				}

				slog.Info("Note Tags", "tags", append(settings.AdditionalTags, tagName))
			}

			// Process the file and collect failed notes
			failedNotes := processEnexFile(cmd, inputFile, filePath, settings)

			// Add failed notes from this file to the collection
			allFailedNotes = append(allFailedNotes, failedNotes...)
		}

		// Process pending tasks and link documents after all files are processed
		logging.SetWorkerLogger(0) // Use main process ID for this
		slog.Info("Processing pending tasks and linking documents for all files")
		if err := inputFile.ProcessPendingTasks(); err != nil {
			slog.Error("failed to process pending tasks and link documents", "error", err)
		}

		// Process retries for all failed notes from all files
		if len(allFailedNotes) > 0 {
			slog.Info("Processing retries for all failed notes", "count", len(allFailedNotes))
			processRetries(inputFile, allFailedNotes, settings.OutputFolder, totalNotesAcrossFiles)
		}

		slog.Info("All ENEX files processed successfully")
		return
	}

	// Single ENEX file processing mode

	// Check if we need to add the filename as a tag
	if settings.UseFilenameAsTag && len(args) > 0 {
		// Extract filename without path and extension
		baseName := filepath.Base(args[0])
		tagName := strings.TrimSuffix(baseName, filepath.Ext(baseName))

		// Add to additional tags if not already added by the command-line flag
		if !contains(settings.AdditionalTags, tagName) {
			newTags := append(settings.AdditionalTags, tagName)
			config.SetAdditionalTags(newTags)
		}
	}

	// Ensure we have a file to process
	if len(args) == 0 {
		slog.Error("No ENEX file specified")
		os.Exit(1)
	}

	filePath := args[0]
	inputFile := enex.NewEnexFile()

	// For single file mode, count notes and set as both file total and overall total
	totalNotes, err := inputFile.CountNotes(filePath)
	if err != nil {
		slog.Error("failed to count notes in file", "error", err)
	} else {
		// Store the total in the atomic counter for overall progress
		inputFile.TotalNotesAll.Store(uint32(totalNotes))
		// Reset the overall counter before processing
		inputFile.CurrentNoteAll.Store(0)
	}

	// Process the single file and collect failed notes
	failedNotes := processEnexFile(cmd, inputFile, filePath, settings)

	// Process pending tasks and link documents
	logging.SetWorkerLogger(0) // Use main process ID for this
	slog.Info("Processing pending tasks and linking documents")
	if err := inputFile.ProcessPendingTasks(); err != nil {
		slog.Error("failed to process pending tasks and link documents", "error", err)
	}

	// Process retries for failed notes
	if len(failedNotes) > 0 {
		slog.Info("Processing retries for failed notes", "count", len(failedNotes))
		processRetries(inputFile, failedNotes, settings.OutputFolder, totalNotes)
	}

	slog.Info("ENEX processing done")
}

// findEnexFiles finds all .enex files in the specified directory (non-recursive)
func findEnexFiles(dirPath string) ([]string, error) {
	var enexFiles []string

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("error reading directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue // Skip subdirectories
		}

		// Check if the file has .enex extension
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".enex") {
			enexFiles = append(enexFiles, filepath.Join(dirPath, entry.Name()))
		}
	}

	return enexFiles, nil
}

// processEnexFile processes a single ENEX file
func processEnexFile(cmd *cobra.Command, inputFile *enex.EnexFile, filePath string, settings config.Config) []enex.Note {
	// Count total notes in the file
	totalNotes, err := inputFile.CountNotes(filePath)
	if err != nil {
		slog.Error("failed to count notes in file", "error", err)
		totalNotes = 0 // Use 0 as fallback if counting fails
	}

	// Reset the current note counter for this file only
	inputFile.CurrentNote.Store(0)

	// prepare channels
	noteChannel := make(chan enex.Note)
	failedNoteChannel := make(chan enex.Note)
	failedNoteSignal := make(chan bool)

	// Failure Catcher
	var failedNotes []enex.Note
	go func() {
		// Register failed note catcher
		logging.SetWorkerLogger(0)
		slog.Debug("Starting failed note catcher")
		enex.FailedNoteCatcher(failedNoteChannel, &failedNotes)
		slog.Debug("Failed note catcher finished")
		failedNoteSignal <- true
	}()

	// Producer
	go func() {
		// Register producer
		logging.SetWorkerLogger(0)
		slog.Info("Starting ENEX file processing", "file", filePath, "total_notes", totalNotes)
		err := inputFile.ReadFromFile(filePath, noteChannel)
		if err != nil {
			slog.Error("failed to read from file", "error", err)
			os.Exit(1)
		}
		slog.Info("Finished reading ENEX file")
	}()

	// Consumers
	var wg sync.WaitGroup
	howMany := settings.ConcurrentWorkers
	if howMany <= 0 {
		howMany = 1 // Default to 1 if not set in config
	}

	wg.Add(howMany)

	for i := 0; i < howMany; i++ {
		workerID := i + 1 // Using 1-based worker IDs for readability

		go func(id int) {
			// Register worker with its ID
			logging.SetWorkerLogger(id)
			slog.Debug("Starting worker", "id", id)

			err := inputFile.UploadFromNoteChannel(noteChannel, failedNoteChannel, settings.OutputFolder, totalNotes)
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
	slog.Info("ENEX file processing done",
		slog.Int("numberOfNotes", int(inputFile.NumNotes.Load())),
		slog.Int("totalFiles", int(inputFile.Uploads.Load())),
	)

	// Return failed notes instead of processing them immediately
	return failedNotes
}

// processRetries processes all failed notes from all files
func processRetries(inputFile *enex.EnexFile, allFailedNotes []enex.Note, outputFolder string, totalNotes int) {
	// If no failed notes, return early
	if len(allFailedNotes) == 0 {
		return
	}

	failedNotes := allFailedNotes

	for {
		// if we still have failedNotes in this iteration, keep going
		if len(failedNotes) == 0 {
			break
		}

		slog.Warn("there have been errors, starting retry cycle", "errors", len(failedNotes))
		PressKeyToContinue()

		// Reset only the per-file counter for the retry cycle
		inputFile.CurrentNote.Store(0)
		// Don't reset the overall counter (CurrentNoteAll) as we're still processing the same notes

		// all failed notes are now in failedNotes slice
		// push notes that failed this Cycle into failedThisCycle slice
		failedThisCycle := []enex.Note{}

		// reset failedNoteChannel
		failedNoteChannel := make(chan enex.Note)
		failedNoteSignal := make(chan bool)

		// this feeds the failedNotes slice into the failedNoteChannel
		go func() {
			// Register retry failed note catcher
			logging.SetWorkerLogger(0)
			slog.Debug("Starting retry failed note catcher")
			enex.FailedNoteCatcher(failedNoteChannel, &failedThisCycle)
			slog.Debug("Retry failed note catcher finished")
			failedNoteSignal <- true
		}()

		// this feeds the failedNotes into the Retry Channel
		retryChannel := make(chan enex.Note)
		go func() {
			// Register retry feeder
			logging.SetWorkerLogger(0)
			slog.Debug("Starting retry feeder")
			enex.RetryFeeder(&failedNotes, retryChannel)
			slog.Debug("Retry feeder finished")
		}()

		// this works on the retry channel
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			// Register retry worker with ID 999
			logging.SetWorkerLogger(999)
			slog.Info("Starting retry worker")
			err := inputFile.UploadFromNoteChannel(retryChannel, failedNoteChannel, outputFolder, totalNotes)
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
