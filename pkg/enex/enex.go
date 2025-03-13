package enex

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/spf13/afero"
)

type EnexFile struct {
	EnExport
	Fs                afero.Fs
	client            *http.Client
	NumNotes, Uploads atomic.Uint32
	documentIDs       map[string][]int // Maps note title to list of document IDs
	taskTracker       *TaskTracker     // Tracks document upload tasks
}

func NewEnexFile() *EnexFile {
	return &EnexFile{
		Fs: afero.NewOsFs(),
		client: &http.Client{
			Timeout: time.Second * 10,
		},
		documentIDs: make(map[string][]int),
	}
}

type EnExport struct {
	ExportDate  string `xml:"export-date,attr"`
	Application string `xml:"application,attr"`
	Version     string `xml:"version,attr"`
	Notes       []Note `xml:"note"`
}

type Note struct {
	Title          string     `xml:"title"`
	Content        string     `xml:"content"`
	Created        string     `xml:"created"`
	Updated        string     `xml:"updated"`
	Tags           []string   `xml:"tag"`
	NoteAttributes NoteAttr   `xml:"note-attributes"`
	Resources      []Resource `xml:"resource"`
}

type NoteAttr struct {
	Location string `xml:"location"`
	// Add other attributes here
}

type Resource struct {
	Data               string             `xml:"data"`
	Mime               string             `xml:"mime"`
	Width              int                `xml:"width,omitempty"`
	Height             int                `xml:"height,omitempty"`
	ResourceAttributes ResourceAttributes `xml:"resource-attributes"`
}

type ResourceAttributes struct {
	SourceURL       string  `xml:"source-url,omitempty"`
	Timestamp       string  `xml:"timestamp,omitempty"`
	FileName        string  `xml:"file-name,omitempty"`
	Latitude        float64 `xml:"latitude,omitempty"`
	Longitude       float64 `xml:"longitude,omitempty"`
	Altitude        float64 `xml:"altitude,omitempty"`
	CameraMake      string  `xml:"camera-make,omitempty"`
	CameraModel     string  `xml:"camera-model,omitempty"`
	Attachment      bool    `xml:"attachment,omitempty"`
	ApplicationData string  `xml:"application-data,omitempty"`
}

// GetTitle returns the note's title
func (n Note) GetTitle() string {
	return n.Title
}

// GetCreated returns the note's created date
func (n Note) GetCreated() string {
	return n.Created
}

// GetTags returns the note's tags
func (n Note) GetTags() []string {
	return n.Tags
}
