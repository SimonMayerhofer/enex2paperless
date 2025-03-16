package config

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/go-playground/validator/v10"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

var (
	once     sync.Once
	settings Config
	initErr  error
	k        = koanf.New(".")
)

type Config struct {
	PaperlessAPI           string   `validate:"required,http_url"`
	Username               string   `validate:"required_with=Password"`
	Password               string   `validate:"required_with=Username"`
	Token                  string   `validate:"required_without=Password"`
	FileTypes              []string `validate:"required"`
	ExcludeFileTypes       []string // File types to exclude when using "any" in FileTypes
	OutputFolder           string
	ExcludedOutputFolder   string // Folder to store excluded files that don't match allowed file types
	NoNameFolder           string // Folder to store files with no original filename
	AdditionalTags         []string
	Unzip                  bool
	LinkFieldID            int    // Custom field ID for document linking
	TitlePrefix            bool   // Whether to prefix filenames with note titles
	ConvertMarkdown        bool   // Whether to convert note content to markdown
	ConvertAppleToPDF      bool   // Whether to convert Apple iWork files to PDF
	ConcurrentWorkers      int    // Number of concurrent workers
	UseFilenameAsTag       bool   // Whether to use the ENEX filename as a tag
	CorrespondentTagPrefix string // Prefix for tags to be used as correspondents
	MaxRetries             int    // Maximum number of retry attempts for failed notes
	AutoRetry              bool   // Whether to automatically retry failed notes without prompting
}

// GetConfig initializes and returns the application configuration.
// It reads from a YAML file and overrides with environment variables if they exist.
// The function ensures that the configuration is loaded only once to maintain consistency
// throughout the application's lifecycle. If the configuration is invalid or cannot be
// loaded, an error will be returned.
func GetConfig() (Config, error) {
	once.Do(func() {
		// Set default values
		settings.MaxRetries = 1
		settings.AutoRetry = true

		// Load YAML configuration
		err := k.Load(file.Provider("config.yaml"), yaml.Parser())
		if err != nil {
			slog.Debug("couldn't read config.yaml", "error", err)
		}

		// Load Environment Variables and override YAML settings
		err = k.Load(env.Provider("", ".", func(s string) string {
			return s
		}), nil)
		if err != nil {
			initErr = fmt.Errorf("configuration error: %v", err)
		}

		// Unmarshal into struct
		if err := k.Unmarshal("", &settings); err != nil {
			initErr = fmt.Errorf("configuration error: %v", err)
			return
		}

		// Validate Config
		validate := validator.New()

		err = validate.Struct(settings)
		if err != nil {

			var validateErrs validator.ValidationErrors
			if errors.As(err, &validateErrs) {
				for _, e := range validateErrs {
					switch e.StructField() {
					case "Token":
						initErr = fmt.Errorf("bad auth config: need either token or username/password")
					case "Username":
						initErr = fmt.Errorf("if using password, username is required too")
					case "Password":
						initErr = fmt.Errorf("if using username, password is required too")
					default:
						initErr = fmt.Errorf("field %s: %s validation failed", e.Field(), e.Tag())
					}
					return
				}
			}
			initErr = fmt.Errorf("configuration error: %v", err)
			return
		}
	})
	return settings, initErr
}

func SetOutputFolder(path string) error {
	settings.OutputFolder = path
	return nil
}

func SetAdditionalTags(tags []string) error {
	settings.AdditionalTags = tags
	return nil
}

func SetUnzip(unzip bool) error {
	settings.Unzip = unzip
	return nil
}

// SetLinkFieldID sets the custom field ID for document linking
func SetLinkFieldID(id int) {
	settings.LinkFieldID = id
}

// SetTitlePrefix sets whether to prefix filenames with note titles
func SetTitlePrefix(prefix bool) {
	settings.TitlePrefix = prefix
}

// SetConvertMarkdown sets whether to convert note content to markdown
func SetConvertMarkdown(convert bool) {
	settings.ConvertMarkdown = convert
}

// SetConvertAppleToPDF sets whether to convert Apple iWork files to PDF
func SetConvertAppleToPDF(convert bool) {
	settings.ConvertAppleToPDF = convert
}

// SetConcurrentWorkers sets the number of concurrent workers
func SetConcurrentWorkers(workers int) {
	settings.ConcurrentWorkers = workers
}

// SetUseFilenameAsTag sets whether to use the ENEX filename as a tag
func SetUseFilenameAsTag(useFilenameAsTag bool) {
	settings.UseFilenameAsTag = useFilenameAsTag
}

// SetCorrespondentTagPrefix sets the prefix for tags to be used as correspondents
func SetCorrespondentTagPrefix(prefix string) {
	settings.CorrespondentTagPrefix = prefix
}

// SetExcludeFileTypes sets the file types to exclude when using "any" in FileTypes
func SetExcludeFileTypes(excludeFileTypes []string) {
	settings.ExcludeFileTypes = excludeFileTypes
}

// SetNoNameFolder sets the folder path for files with no original filename
func SetNoNameFolder(folder string) {
	settings.NoNameFolder = folder
}

// SetExcludedOutputFolder sets the folder for storing excluded files.
func SetExcludedOutputFolder(folder string) {
	settings.ExcludedOutputFolder = folder
}

// SetMaxRetries sets the maximum number of retry attempts
func SetMaxRetries(retries int) {
	settings.MaxRetries = retries
}

// SetAutoRetry sets whether to automatically retry failed notes
func SetAutoRetry(autoRetry bool) {
	settings.AutoRetry = autoRetry
}
