# enex2paperless

## Description

CLI tool to help migrate attachements from Evernote notes to Paperless-NGX. It parses ENEX files and uses the Paperless API to upload the contents. It goes through the ENEX file, looks for notes containing allowed file types and extracts those files and uploads them to Paperless.

This tool was initially created by [kevinzehnder](https://github.com/kevinzehnder/enex2paperless/) but I added some additional features:

- ✅ **Link documents** from the same note together using a custom field.
- ✅ **Convert note content** to Markdown format. So not only attachments are uploaded but also the content.
- ✅ **Convert Apple iWork files** (Pages, Numbers, Keynote) to PDF format (optional, macOS only) before uploading.
- ✅ **Add additional tags** to all documents processed.
- ✅ **ENEX filename as an additional tag** for all documents uploaded.
- ✅ **Import Tags with Prefix as Correspondent** e.g. "@google" -> Correspondent: "Google"
- ✅ **Automatically extract any zip files** found in the notes.
- ✅ **Preserver original file creation times**, so that all files have the right date.
- ✅ **Add filename to title** for notes with more than 2 attachments. So not all documents from a note have the same name.
- ✅ **Prefix filenames with note title** when using output to folder option `Notetitle - filename.pdf`
- ✅ **Add suffix for filename collisions** using output to folder option. E.g. scan-1.pdf, scan-2.pdf, etc.
- ✅ **Fix file extensions for exported files** in case the evernote exported files have the wrong or missing extensions.
- ✅ **Add option to exclude specific file types** when using the "any" option in FileTypes.
- ✅ **Save excluded files** to separate folder.

Planned features:
- ⚪ Save files from notes witch had errors to folder

## How To Use

```shell
Usage:
  enex2paperless [file path] [flags]

Flags:
  -c, --concurrent int           Number of concurrent consumers (default 1)
      --convert-apple-pdf        Convert Apple iWork files to PDF
  -e, --excluded-outputfolder    Folder to save excluded files
  -m, --convert-markdown         Convert note content to markdown format
      --correspondent-tag-prefix Prefix for tags to be used as correspondents
  -h, --help                     help for enex2paperless
  -l, --link int                 Custom field ID for document linking
  -n, --nocolor                  Disable colored output
      --noname string            Folder to save files with no original filename
  -o, --outputfolder string      Output attachements to this folder, NOT paperless.
  -t, --tags strings             Additional tags to add to all documents
  -T, --use-filename-tag         Add the ENEX filename as tag to all documents
      --titleprefix              Prefix filenames with note titles when using outputfolder
  -u, --unzip                    Unzip .zip files found in notes
  -v, --verbose                  Enable verbose logging
```

- Export your Notes from Evernote to an ENEX file, e.g. `MyEnexFile.enex`
- Download `enex2paperless.zip` from [Releases](https://github.com/SimonMayerhofer/enex2paperless/releases/latest).
- Extract files to the same location as your ENEX file.
- Edit `config.yaml` (see below) and add your personal information. This depends on your installation of Paperless.
- Run `enex2paperless MyEnexFile.enex`

To authenticate against Paperless, you can either use a token or a username/password combination. Don't configure both variations at the same time.

### Configuration Options

All settings can be configured either in the `config.yaml` file or via command-line arguments. If a setting is specified both in the config file and as a command-line argument, the command-line argument takes precedence.

#### Config File

The `config.yaml` file supports the following settings:

```yaml
# Required settings
PaperlessAPI: http://paperboy.lan:8000  # URL of your Paperless instance
Username: user                          # Username for Paperless (use with Username)
Password: pass                          # Password for Paperless (use with Password)
Token:                                  # API token for Paperless (alternative to Username/Password)
FileTypes:                              # List of file types to process
  - pdf
  - txt
  - jpg

# Optional settings
ExcludeFileTypes:           # File types to exclude when using "any" in FileTypes
  - gif
  - svg
  - psd
AdditionalTags: []          # Additional tags to add to all documents
ConvertMarkdown: false      # Whether to convert note content to markdown
ConvertAppleToPDF: false    # Whether to convert Apple iWork files to PDF
ConcurrentWorkers: 1        # Number of concurrent workers
CorrespondentTagPrefix: ""  # Prefix for tags to be used as correspondents
ExcludedOutputFolder: ""    # Folder to save excluded files
LinkFieldID: 0              # Custom field ID for document linking
NoNameFolder: ""            # Folder to save files with no original filename
OutputFolder: ""            # Output to folder instead of Paperless
TitlePrefix: false          # Whether to prefix filenames with note titles
Unzip: false                # Whether to unzip .zip files found in notes
UseFilenameAsTag: false     # Whether to use the ENEX filename as a tag
```

#### Command-line Arguments

The following table shows the mapping between config.yaml settings and command-line arguments:

| config.yaml setting    | Command-line argument       | Description |
|------------------------|-----------------------------|-------------|
| AdditionalTags         | -t, --tags                  | Additional tags to add to all documents |
| ConvertMarkdown        | -m, --convert-markdown      | Convert note content to markdown |
| ConvertAppleToPDF      | --convert-apple-pdf         | Convert Apple iWork files to PDF |
| ConcurrentWorkers      | -c, --concurrent            | Number of concurrent workers |
| CorrespondentTagPrefix | --correspondent-tag-prefix  | Prefix for tags to be used as correspondents |
| ExcludedOutputFolder   | -e, --excluded-outputfolder | Folder to save excluded files |
| LinkFieldID            | -l, --link                  | Custom field ID for document linking |
| NoNameFolder           | --noname                    | Folder to save files with no original filename |
| OutputFolder           | -o, --outputfolder          | Output attachements to this folder, NOT paperless |
| TitlePrefix            | --titleprefix               | Prefix filenames with note titles |
| Unzip                  | -u, --unzip                 | Unzip .zip files found in notes |
| UseFilenameAsTag       | -T, --use-filename-tag      | Add the ENEX filename as tag to all documents |

## Explanation of Configuration Options

### Allowed FileTypes - required

You can select which file types should be processed. The MIME type of the Evernote attachements will be compared with the configured file types. If there's no match, the attachement will be ignored. This avoids trying to upload unwanted or unsupported filetypes.

If you want to upload any attachements, regardless, then you can include `any` in the list of filetypes, like this:

```yaml
FileTypes:
  - any
```

If you want to use the `any` option but exclude specific file types, you can use the `ExcludeFileTypes` setting:

```yaml
FileTypes:
  - any
ExcludeFileTypes:
  - jpg
  - exe
  - psd
  - ai
```

This will process all file types except for the ones listed in `ExcludeFileTypes`. The excluded files will also be skipped when extracting zip files.

### Multiple Concurrent Uploads

The tool is capable of handling multiple uploads concurrently. By default it will process the attachements one by one. You can use the `-c` flag to configure multiple workers, like this:

```shell
enex2paperless.exe MyEnexFile.enex -c 3
```

Alternatively, you can set this in your `config.yaml` file:

```yaml
ConcurrentWorkers: 3
```

> **Attention:** Depending on your Paperless installation, it might not be able to handle multiple requests at the same time efficiently. In that case, using multiple concurrent uploads would only slow down the process instead of speeding it up.

### Output To Folder

Optionally it is possible to output all attachements to a specific folder, as opposed to uploading them to Paperless. If you want to use Enex2Paperless in that mode, then you have to provide a foldername:

```shell
enex2paperless.exe MyEnexFile.enex -o myfoldername
```

Alternatively, you can set this in your `config.yaml` file:

```yaml
OutputFolder: "myfoldername"
```

This disables uploads to Paperless and only outputs files to your provided folder.

You can also prefix filenames with their note titles using the `--titleprefix` flag:

```shell
enex2paperless.exe MyEnexFile.enex -o myfoldername --titleprefix
```

Or in your `config.yaml` file:

```yaml
OutputFolder: "myfoldername"
TitlePrefix: true
```

This will save files as "Note Title - Filename.ext". If the note title and filename (without extension) are identical, the prefix will be skipped to avoid duplication.

### Additional Tags / Filename As Tag

You can add additional tags to all files being processed using the `-t` flag and a comma separated list of strings:

```shell
enex2paperless.exe MyEnexFile.enex -t migration2024,taxes,important
```

Alternatively, you can set this in your `config.yaml` file:

```yaml
AdditionalTags:
  - migration2024
  - taxes
  - important
```

If you set the `-T` or `--use-filename-tag` flag, the ENEX filename (without extension) will be used as an additional tag:

```shell
enex2paperless.exe MyEnexFile.enex -T
```

Alternatively, you can set this in your `config.yaml` file:

```yaml
UseFilenameAsTag: true
```

This will add "MyEnexFile" as a tag to all processed files.

If you use neither the `-t` or `-T` flags, no additional tags will be added, and only the original Evernote tags will be preserved.

### Tags as Correspondents

You can automatically convert tags with a specific prefix to Paperless correspondents. This is useful if you use tags in Evernote to mark the sender or recipient of a document.

To enable this feature, use the `--correspondent-tag-prefix` flag followed by the prefix you use (e.g., "@"):

```shell
enex2paperless.exe MyEnexFile.enex --correspondent-tag-prefix "@"
```

Alternatively, you can set this in your `config.yaml` file:

```yaml
CorrespondentTagPrefix: "@"
```

When this feature is enabled:

1. The first tag with the specified prefix will be used as the correspondent
2. The prefix will be removed from the name when creating the correspondent
3. The correspondent name will be formatted with proper capitalization (e.g., "@john doe" becomes "John Doe")
4. If multiple tags have the prefix, only the first one will be used as a correspondent; the others will be kept as regular tags
5. If a correspondent with the same name already exists, it will be reused

For example, if your note has tags ["@john doe", "invoice", "@jane smith"], and you set the correspondent tag prefix to "@", then:
- "John Doe" will be set as the correspondent
- "invoice" will be kept as a tag
- "@jane smith" will be kept as a tag (with the "@" prefix)

### Unzip Attachments

You can automatically extract any zip files found in the notes using the `-u` or `--unzip` flag:

```shell
enex2paperless.exe MyEnexFile.enex -u
```

Alternatively, you can set this in your `config.yaml` file:

```yaml
Unzip: true
```

If using the Output To Folder functionality this will create a subfolder for each zip file, named after the zip file (without the .zip extension), and extract its contents there.

### Document Linking

You can link documents from the same note together using a custom field of type "Document Link". This is useful when you have multiple documents in a single note that are related to each other.

First, create a custom field of type "Document Link" in your Paperless-ngx instance. You can find the ID of the custom field in the Paperless-ngx UI or by calling the API endpoint `/api/custom_fields/`.

Then use the `-l` or `--link` flag to specify the custom field ID:

```shell
enex2paperless.exe MyEnexFile.enex -l 1
```

Alternatively, you can set this in your `config.yaml` file:

```yaml
LinkFieldID: 1
```

This will:
1. Upload all documents from each note
2. For each document, create links to all other documents from the same note
3. The links will be stored in the specified custom field
4. Works for both individual files and files extracted from zip archives

### Markdown Conversion

You can convert the content of your Evernote notes to Markdown format using the `-m` or `--convert-markdown` flag:

```shell
enex2paperless.exe MyEnexFile.enex -m
```

Alternatively, you can set this in your `config.yaml` file:

```yaml
ConvertMarkdown: true
```

Files will be tagged with "markdown" for easy identification. Only notes with text in the content will be uploaded as markdown files.

### NoName Folder for Files Without Filenames

Some attachments in Evernote notes might not have a filename set. By default, the tool uses the note title as the filename when this happens. You can optionally specify a separate folder to store these files using the `NoNameFolder` setting.

You can set this in your `config.yaml` file:

```yaml
NoNameFolder: "noname"
```

Or use the command-line flag:

```shell
enex2paperless.exe MyEnexFile.enex --noname noname
```

If you saved webpages with the Evernote webclipper or forwarded your mails you might have a lot of (unnamed) images which you don't want to have in Evernote. These will be saved in the NoNameFolder instead of being uploaded to Paperless.

The idea is to find those files in a first run when using the outputfolder option. If these are actual files which you want to keep rename the file in the Evernote Note, save the enex again and upload it to paperless.

When this setting is configured:
1. Files without original filenames will be saved to this folder instead of being uploaded to Paperless.
2. The note title will still be used as the filename, but they'll be stored separately

If not specified, files with no name will be saved in the regular output folder or will be uploaded to Paperless.

### Excluded Files Output Folder

When processing files, some attachments might be excluded based on your `FileTypes` and `ExcludeFileTypes` settings. By default, these files are simply skipped. However, you can save these excluded files to a dedicated folder using the `ExcludedOutputFolder` setting.

You can set this in your `config.yaml` file:

```yaml
ExcludedOutputFolder: "excluded"
```

Or use the command-line flag:

```shell
enex2paperless.exe MyEnexFile.enex --excluded-outputfolder excluded
```

This is particularly useful for:
1. Reviewing what files were excluded during processing
2. Ensuring no important files are missed
3. Finding files you might want to include in future runs

This feature works alongside both the regular Paperless upload mode and the `OutputFolder` option.

### Apple iWork Files Conversion

You can automatically convert Apple iWork files (Pages, Numbers, and Keynote) to PDF format before uploading them to Paperless. This feature is only available on macOS and requires the corresponding Apple applications to be installed:

- Pages for .pages files
- Numbers for .numbers files
- Keynote for .key files

To enable this feature, add the following to your `config.yaml`:

```yaml
ConvertAppleToPDF: true
```

Or use the command-line flag:

```shell
enex2paperless.exe MyEnexFile.enex --convert-apple-pdf
```

This feature is particularly useful for Evernote users who have stored Apple iWork documents in their notes, as Paperless doesn't natively support these formats.

If `ConvertAppleToPDF` is not enabled, Apple iWork files will be skipped during processing to avoid upload errors.

### Verbose Logging

If you're running into problems, you can enable a more verbose log output by using the `-v` flag. This should help troubleshoot the problems.

### NoColor

If your console doesn't support colored output using ANSI escape codes, the output will look messed up, similar to this:

```shell
←[38;2;224;175;104m[08:46:55.211]←[0m [←[38;2;158;206;105m INFO←[0m] ←[38;2;192;202;245mprocessing file←[0m ←[38;2;187;154;247m{"file":"test.pdf"}←[0m
←[38;2;224;175;104m[08:46:56.734]←[0m [←[38;2;158;206;105m INFO←[0m] ←[38;2;192;202;245mENEX processing done←[0m ←[38;2;187;154;247m{"numberOfNotes":1,"totalFiles":1}←[0m
←[38;2;224;175;104m[08:46:56.734]←[0m [←[38;2;158;206;105m INFO←[0m] ←[38;2;192;202;245mall notes processed successfully←[0m
```

If that's the case, enable the `-n` flag to disable colored output.
