package enex

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/spf13/afero"
)

// TaskInfo represents information about a document upload task and its linking data
type TaskInfo struct {
	TaskID        string   `json:"task_id"`
	Title         string   `json:"title"`
	FileName      string   `json:"file_name"`
	NoteTitle     string   `json:"note_title"`
	DocumentID    int      `json:"document_id,omitempty"`
	Status        string   `json:"status"`
	RelatedDocIDs []int    `json:"related_doc_ids,omitempty"`
	DateCreated   string   `json:"date_created"`
	DateDone      string   `json:"date_done,omitempty"`
}

// TaskTracker manages the tracking of document upload tasks
type TaskTracker struct {
	Tasks []TaskInfo `json:"tasks"`
	File  string     `json:"-"`
	Fs    afero.Fs   `json:"-"`
	mu    sync.Mutex // Mutex for protecting concurrent access
}

// NewTaskTracker creates a new task tracker
func NewTaskTracker(file string) *TaskTracker {
	return &TaskTracker{
		Tasks: make([]TaskInfo, 0),
		File:  file,
		Fs:    afero.NewOsFs(),
		mu:    sync.Mutex{},
	}
}

// AddTask adds a new task to the tracker
func (t *TaskTracker) AddTask(task TaskInfo) error {
	t.mu.Lock()
	t.Tasks = append(t.Tasks, task)
	tasks := t.Tasks // Make a copy of tasks
	t.mu.Unlock()

	// Save without holding the lock
	data, err := json.MarshalIndent(&TaskTracker{Tasks: tasks}, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling tasks: %v", err)
	}
	return afero.WriteFile(t.Fs, t.File, data, 0644)
}

// UpdateTask updates an existing task
func (t *TaskTracker) UpdateTask(taskID string, updates TaskInfo) error {
	t.mu.Lock()
	var found bool
	for i, task := range t.Tasks {
		if task.TaskID == taskID {
			t.Tasks[i] = updates
			found = true
			break
		}
	}
	tasks := t.Tasks // Make a copy of tasks
	t.mu.Unlock()

	if !found {
		return fmt.Errorf("task not found: %s", taskID)
	}

	// Save without holding the lock
	data, err := json.MarshalIndent(&TaskTracker{Tasks: tasks}, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling tasks: %v", err)
	}
	return afero.WriteFile(t.Fs, t.File, data, 0644)
}

// Save saves the tasks to the JSON file
func (t *TaskTracker) Save() error {
	t.mu.Lock()
	tasks := t.Tasks // Make a copy of tasks
	t.mu.Unlock()

	data, err := json.MarshalIndent(&TaskTracker{Tasks: tasks}, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling tasks: %v", err)
	}
	return afero.WriteFile(t.Fs, t.File, data, 0644)
}

// Load loads tasks from the JSON file
func (t *TaskTracker) Load() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	data, err := afero.ReadFile(t.Fs, t.File)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("error reading tasks file: %v", err)
	}
	return json.Unmarshal(data, t)
}

// GetPendingTasks returns all tasks that haven't been completed
func (t *TaskTracker) GetPendingTasks() []TaskInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	var pending []TaskInfo
	for _, task := range t.Tasks {
		if task.Status != "SUCCESS" && task.Status != "FAILURE" {
			pending = append(pending, task)
		}
	}
	return pending
}

// GetTasksByNote returns all tasks for a specific note
func (t *TaskTracker) GetTasksByNote(noteTitle string) []TaskInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	var tasks []TaskInfo
	for _, task := range t.Tasks {
		if task.NoteTitle == noteTitle {
			tasks = append(tasks, task)
		}
	}
	return tasks
}