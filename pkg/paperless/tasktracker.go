package paperless

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"enex2paperless/internal/config"

	"github.com/spf13/afero"
)

// TaskTrackerImpl is the concrete implementation of the TaskTracker interface
type TaskTrackerImpl struct {
	Tasks []TaskInfo `json:"tasks"`
	File  string     `json:"-"`
	Fs    afero.Fs   `json:"-"`
	mu    sync.Mutex // Mutex for protecting concurrent access
}

// NewTaskTracker creates a new task tracker
func NewTaskTracker(file string) *TaskTrackerImpl {
	return &TaskTrackerImpl{
		Tasks: make([]TaskInfo, 0),
		File:  file,
		Fs:    afero.NewOsFs(),
		mu:    sync.Mutex{},
	}
}

// AddTask adds a new task to the tracker
func (t *TaskTrackerImpl) AddTask(task TaskInfo) error {
	t.mu.Lock()
	t.Tasks = append(t.Tasks, task)
	tasks := t.Tasks // Make a copy of tasks
	t.mu.Unlock()

	// Save without holding the lock
	data, err := json.MarshalIndent(&TaskTrackerImpl{Tasks: tasks}, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling tasks: %v", err)
	}
	return afero.WriteFile(t.Fs, t.File, data, 0644)
}

// UpdateTask updates an existing task
func (t *TaskTrackerImpl) UpdateTask(taskID string, updates TaskInfo) error {
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
	data, err := json.MarshalIndent(&TaskTrackerImpl{Tasks: tasks}, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling tasks: %v", err)
	}
	return afero.WriteFile(t.Fs, t.File, data, 0644)
}

// Save saves the tasks to the JSON file
func (t *TaskTrackerImpl) Save() error {
	t.mu.Lock()
	tasks := t.Tasks // Make a copy of tasks
	t.mu.Unlock()

	data, err := json.MarshalIndent(&TaskTrackerImpl{Tasks: tasks}, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling tasks: %v", err)
	}
	return afero.WriteFile(t.Fs, t.File, data, 0644)
}

// Load loads tasks from the JSON file
func (t *TaskTrackerImpl) Load() error {
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
func (t *TaskTrackerImpl) GetPendingTasks() []TaskInfo {
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
func (t *TaskTrackerImpl) GetTasksByNote(noteTitle string) []TaskInfo {
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

// ProcessPendingTasks checks the status of pending tasks and updates their status
func ProcessPendingTasks(taskTracker *TaskTrackerImpl, client *http.Client) error {
	settings, _ := config.GetConfig()
	pendingTasks := taskTracker.GetPendingTasks()

	// Keep checking until all tasks are done
	for len(pendingTasks) > 0 {
		slog.Info("checking pending tasks", "count", len(pendingTasks))

		for _, task := range pendingTasks {
			url := fmt.Sprintf("%s/api/tasks/?task_id=%s", settings.PaperlessAPI, task.TaskID)
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				slog.Error("error creating GET request", "error", err)
				continue
			}

			if settings.Token != "" {
				req.Header.Set("Authorization", "Token "+settings.Token)
			} else {
				req.SetBasicAuth(settings.Username, settings.Password)
			}

			resp, err := client.Do(req)
			if err != nil {
				slog.Error("error getting task details", "error", err)
				continue
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				bodyBytes, _ := io.ReadAll(resp.Body)
				slog.Error("error getting task details",
					"status code", resp.StatusCode,
					"response body", string(bodyBytes))
				continue
			}

			bodyBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				slog.Error("error reading response body", "error", err)
				continue
			}

			var taskList []map[string]interface{}
			if err := json.Unmarshal(bodyBytes, &taskList); err != nil {
				slog.Error("error decoding task list", "error", err)
				continue
			}

			if len(taskList) > 0 {
				taskData := taskList[0]
				status, _ := taskData["status"].(string)
				dateDone, _ := taskData["date_done"].(string)
				result, _ := taskData["result"].(string)

				// Update task info
				task.Status = status
				task.DateDone = dateDone

				// Handle duplicate document case
				if status == "FAILURE" && strings.Contains(result, "duplicate") {
					if relatedDoc, ok := taskData["related_document"].(string); ok {
						if docID, err := strconv.Atoi(relatedDoc); err == nil {
							task.DocumentID = docID
							task.Status = "SUCCESS" // Mark as success since we found the duplicate
							slog.Info("found duplicate document",
								"task_id", task.TaskID,
								"original_doc_id", docID,
								"note", task.NoteTitle)
						}
					}
				} else if status == "SUCCESS" {
					if relatedDoc, ok := taskData["related_document"].(string); ok {
						if docID, err := strconv.Atoi(relatedDoc); err == nil {
							task.DocumentID = docID
						}
					}
				}

				if err := taskTracker.UpdateTask(task.TaskID, task); err != nil {
					slog.Error("failed to update task", "error", err)
				}
			}
		}

		// Save tasks to file after each batch of updates
		if err := taskTracker.Save(); err != nil {
			slog.Error("failed to save tasks", "error", err)
		}

		// Get updated list of pending tasks
		pendingTasks = taskTracker.GetPendingTasks()

		// If there are still pending tasks, wait before checking again
		if len(pendingTasks) > 0 {
			slog.Info("waiting for tasks to complete", "pending_count", len(pendingTasks))
			time.Sleep(2 * time.Second) // Wait 2 seconds before checking again
		}
	}

	slog.Info("all tasks completed")
	return nil
}

// LinkDocuments processes all completed tasks and links their documents
func LinkDocumentsFromTasks(taskTracker *TaskTrackerImpl, client *http.Client) error {
	settings, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("error getting config: %v", err)
	}

	if settings.LinkFieldID == 0 {
		slog.Debug("skipping document linking - no link field ID specified")
		return nil
	}

	// Group tasks by note title
	tasksByNote := make(map[string][]TaskInfo)
	var remainingTasks []TaskInfo

	for _, task := range taskTracker.Tasks {
		// Include both successful uploads and duplicate documents
		if (task.Status == "SUCCESS" || (task.Status == "FAILURE" && task.DocumentID > 0)) && task.DocumentID > 0 {
			tasksByNote[task.NoteTitle] = append(tasksByNote[task.NoteTitle], task)
		} else {
			remainingTasks = append(remainingTasks, task)
		}
	}

	// Process each note's documents
	for noteTitle, tasks := range tasksByNote {
		if len(tasks) < 2 {
			slog.Debug("skipping document linking - less than 2 documents",
				"note", noteTitle,
				"count", len(tasks))
			continue
		}

		// Extract document IDs
		var documentIDs []int
		for _, task := range tasks {
			documentIDs = append(documentIDs, task.DocumentID)
		}

		// Link documents
		if err := linkDocuments(noteTitle, documentIDs, client); err != nil {
			slog.Error("failed to link documents",
				"error", err,
				"note", noteTitle,
				"document_ids", documentIDs)
			// If linking failed, keep these tasks
			remainingTasks = append(remainingTasks, tasks...)
		} else {
			slog.Debug("successfully linked documents",
				"note", noteTitle,
				"count", len(documentIDs),
				"document_ids", documentIDs)
		}
	}

	// Update the task tracker with remaining tasks
	taskTracker.Tasks = remainingTasks

	// If no tasks remain, delete the tasks.json file
	if len(remainingTasks) == 0 {
		// Check if the file exists before trying to remove it
		exists, err := afero.Exists(taskTracker.Fs, taskTracker.File)
		if err != nil {
			slog.Error("failed to check if tasks file exists", "error", err)
		} else if exists {
			if err := taskTracker.Fs.Remove(taskTracker.File); err != nil {
				slog.Error("failed to delete tasks file", "error", err)
			} else {
				slog.Info("deleted tasks file - all tasks completed")
			}
		} else {
			slog.Debug("tasks file does not exist, skipping removal", "file", taskTracker.File)
		}
	} else {
		// Save remaining tasks
		if err := taskTracker.Save(); err != nil {
			slog.Error("failed to save remaining tasks", "error", err)
		}
	}

	return nil
}

// linkDocuments links all documents from a note together using the custom field
func linkDocuments(noteTitle string, documentIDs []int, client *http.Client) error {
	if len(documentIDs) < 2 {
		slog.Debug("skipping document linking - less than 2 documents",
			"count", len(documentIDs),
			"document_ids", documentIDs)
		return nil
	}

	settings, err := config.GetConfig()
	if err != nil {
		return fmt.Errorf("error getting config: %v", err)
	}

	if settings.LinkFieldID == 0 {
		slog.Debug("skipping document linking - no link field ID specified")
		return nil
	}

	// Ensure document IDs are unique to prevent self-linking issues
	uniqueIDs := make([]int, 0, len(documentIDs))
	idMap := make(map[int]bool)

	for _, id := range documentIDs {
		if !idMap[id] {
			idMap[id] = true
			uniqueIDs = append(uniqueIDs, id)
		} else {
			slog.Warn("duplicate document ID found - skipping",
				"document_id", id,
				"note", noteTitle)
		}
	}

	// If we have fewer than 2 unique IDs, skip linking
	if len(uniqueIDs) < 2 {
		slog.Warn("skipping document linking - fewer than 2 unique documents after deduplication",
			"original_count", len(documentIDs),
			"unique_count", len(uniqueIDs),
			"document_ids", documentIDs,
			"unique_ids", uniqueIDs,
			"note", noteTitle)
		return nil
	}

	slog.Debug("starting document linking with unique IDs",
		"note", noteTitle,
		"original_count", len(documentIDs),
		"unique_count", len(uniqueIDs),
		"unique_ids", uniqueIDs,
		"link_field_id", settings.LinkFieldID)

	// Use unique IDs for document linking from now on
	documentIDs = uniqueIDs

	// Update each document to link to all others
	for _, id := range documentIDs {
		url := fmt.Sprintf("%s/api/documents/%d/", settings.PaperlessAPI, id)
		slog.Debug("processing document for linking",
			"document_id", id,
			"url", url)

		// Get current document data with retries
		var docData map[string]interface{}
		maxRetries := 5
		retryDelay := 2 * time.Second
		var lastErr error

		for retry := 0; retry < maxRetries; retry++ {
			getReq, err := http.NewRequest("GET", url, nil)
			if err != nil {
				lastErr = fmt.Errorf("error creating GET request: %v", err)
				continue
			}

			if settings.Token != "" {
				getReq.Header.Set("Authorization", "Token "+settings.Token)
			} else {
				getReq.SetBasicAuth(settings.Username, settings.Password)
			}

			slog.Debug("fetching document data", "url", getReq.URL.String(), "retry", retry+1)

			getResp, err := client.Do(getReq)
			if err != nil {
				lastErr = fmt.Errorf("error getting document: %v", err)
				time.Sleep(retryDelay)
				continue
			}
			defer getResp.Body.Close()

			if getResp.StatusCode == 404 {
				lastErr = fmt.Errorf("document not found (404)")
				slog.Warn("document not found, skipping linking",
					"document_id", id,
					"note", noteTitle)
				// Remove this document from the list of documents to link
				documentIDs = removeInt(documentIDs, id)
				break
			}

			if getResp.StatusCode != 200 {
				var bodyBytes []byte
				bodyBytes, _ = io.ReadAll(getResp.Body)
				lastErr = fmt.Errorf("error getting document: status code %d", getResp.StatusCode)
				slog.Error("error getting document",
					"status_code", getResp.StatusCode,
					"response_body", string(bodyBytes),
					"document_id", id)
				time.Sleep(retryDelay)
				continue
			}

			var bodyBytes []byte
			bodyBytes, err = io.ReadAll(getResp.Body)
			if err != nil {
				lastErr = fmt.Errorf("error reading response body: %v", err)
				time.Sleep(retryDelay)
				continue
			}

			if err := json.Unmarshal(bodyBytes, &docData); err != nil {
				lastErr = fmt.Errorf("error decoding document data: %v", err)
				time.Sleep(retryDelay)
				continue
			}

			// If we get here, we successfully got the document data
			break
		}

		if docData == nil {
			slog.Error("failed to get document data after retries",
				"document_id", id,
				"error", lastErr)
			return fmt.Errorf("failed to get document data after %d retries: %v", maxRetries, lastErr)
		}

		// Create array of linked document IDs (excluding current document)
		var linkedIDs []int
		for _, linkedID := range documentIDs {
			if linkedID != id {
				linkedIDs = append(linkedIDs, linkedID)
			}
		}

		slog.Debug("preparing to update document",
			"document_id", id,
			"linked_ids", linkedIDs,
			"total_documents", len(documentIDs))

		// Initialize custom_fields if it doesn't exist
		if _, ok := docData["custom_fields"]; !ok {
			slog.Debug("initializing custom_fields array", "document_id", id)
			docData["custom_fields"] = []interface{}{}
		}

		// Update custom field with links
		customFields, ok := docData["custom_fields"].([]interface{})
		if !ok {
			slog.Error("custom_fields is not an array",
				"type", fmt.Sprintf("%T", docData["custom_fields"]),
				"value", docData["custom_fields"],
				"document_id", id)
			return fmt.Errorf("custom_fields is not an array")
		}

		slog.Debug("current custom fields",
			"document_id", id,
			"custom_fields", customFields)

		// Check if the link field already exists
		fieldExists := false
		for _, field := range customFields {
			if fieldMap, ok := field.(map[string]interface{}); ok {
				slog.Debug("checking custom field",
					"document_id", id,
					"field", fieldMap)
				if fieldID, ok := fieldMap["field"].(float64); ok {
					if int(fieldID) == settings.LinkFieldID {
						fieldMap["value"] = linkedIDs
						fieldExists = true
						slog.Debug("updated existing link field",
							"document_id", id,
							"field_id", settings.LinkFieldID,
							"linked_ids", linkedIDs,
							"updated_field", fieldMap)
						break
					}
				}
			}
		}

		// If the field doesn't exist, add it
		if !fieldExists {
			newField := map[string]interface{}{
				"field": float64(settings.LinkFieldID),
				"value": linkedIDs,
			}
			customFields = append(customFields, newField)
			docData["custom_fields"] = customFields
			slog.Debug("added new link field",
				"document_id", id,
				"field_id", settings.LinkFieldID,
				"linked_ids", linkedIDs,
				"new_field", newField)
		}

		// Send update request
		jsonData, err := json.Marshal(docData)
		if err != nil {
			return fmt.Errorf("error marshaling document data: %v", err)
		}

		patchReq, err := http.NewRequest("PATCH", url, bytes.NewBuffer(jsonData))
		if err != nil {
			return fmt.Errorf("error creating PATCH request: %v", err)
		}

		if settings.Token != "" {
			patchReq.Header.Set("Authorization", "Token "+settings.Token)
		} else {
			patchReq.SetBasicAuth(settings.Username, settings.Password)
		}
		patchReq.Header.Set("Content-Type", "application/json")

		patchResp, err := client.Do(patchReq)
		if err != nil {
			return fmt.Errorf("error updating document: %v", err)
		}
		defer patchResp.Body.Close()

		if patchResp.StatusCode != 200 {
			var bodyBytes []byte
			bodyBytes, _ = io.ReadAll(patchResp.Body)
			slog.Error("error updating document",
				"status_code", patchResp.StatusCode,
				"response_body", string(bodyBytes),
				"request_data", string(jsonData),
				"document_id", id)
			return fmt.Errorf("error updating document: status code %d", patchResp.StatusCode)
		}

		slog.Debug("successfully updated document", "document_id", id)
	}

	slog.Info("successfully linked all documents",
		"note", noteTitle,
		"document_count", len(documentIDs),
		"document_ids", documentIDs,
		"link_field_id", settings.LinkFieldID)
	return nil
}

// Helper function to remove an integer from a slice
func removeInt(slice []int, s int) []int {
	for i, v := range slice {
		if v == s {
			return append(slice[:i], slice[i+1:]...)
		}
	}
	return slice
}