package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/get-vix/vix/internal/daemon/brain"

	_ "modernc.org/sqlite"
)

// generateSessionID creates a random UUID session ID.
func generateSessionID() string {
	return uuid.New().String()
}

// initAccessStatsDB opens or creates the access stats database at dbPath and
// initializes the schema. The parent directory is created if necessary.
func initAccessStatsDB(dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	// Use a single connection for this write-mostly DB.
	// WAL mode allows concurrent reads alongside the writer; the busy timeout
	// makes SQLite retry for up to 5 s instead of returning SQLITE_BUSY immediately.
	db, err := sql.Open("sqlite", dbPath+"?_busy_timeout=5000&_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	// A pool size of 1 eliminates writer-vs-writer lock contention entirely.
	db.SetMaxOpenConns(1)

	// Create file_access table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS file_access (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			path TEXT NOT NULL,
			tool TEXT NOT NULL,
			parameters TEXT,
			timestamp INTEGER NOT NULL
		)
	`)
	if err != nil {
		db.Close()
		return nil, err
	}

	// Create function_access table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS function_access (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			file TEXT NOT NULL,
			function TEXT NOT NULL,
			tool TEXT NOT NULL,
			parameters TEXT,
			timestamp INTEGER NOT NULL
		)
	`)
	if err != nil {
		db.Close()
		return nil, err
	}

	// Add session_id column (idempotent — ignores error if column already exists)
	db.Exec("ALTER TABLE file_access ADD COLUMN session_id TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE function_access ADD COLUMN session_id TEXT NOT NULL DEFAULT ''")

	// Add language column to file_access (idempotent)
	db.Exec("ALTER TABLE file_access ADD COLUMN language TEXT NOT NULL DEFAULT ''")

	// Create indexes for query performance (idempotent)
	db.Exec("CREATE INDEX IF NOT EXISTS idx_file_access_path ON file_access(path)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_file_access_session ON file_access(session_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_function_access_file ON function_access(file)")

	return db, nil
}

// logFileAccess logs a file access event to the database.
func logFileAccess(db *sql.DB, sessionID, path, tool, language string, params map[string]any) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}

	timestamp := time.Now().Unix()
	_, err = db.Exec(
		"INSERT INTO file_access (path, tool, parameters, timestamp, session_id, language) VALUES (?, ?, ?, ?, ?, ?)",
		path, tool, string(paramsJSON), timestamp, sessionID, language,
	)
	return err
}

// logFunctionAccess logs a function access event to the database.
func logFunctionAccess(db *sql.DB, sessionID, file, function, tool string, params map[string]any) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}

	timestamp := time.Now().Unix()
	_, err = db.Exec(
		"INSERT INTO function_access (file, function, tool, parameters, timestamp, session_id) VALUES (?, ?, ?, ?, ?, ?)",
		file, function, tool, string(paramsJSON), timestamp, sessionID,
	)
	return err
}

// languageForFile resolves the programming language for a file path using the extension map.
func languageForFile(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	return brain.LanguageForExt(ext)
}

// logToolAccess is the centralized access logging function.
// It extracts file/function info from tool params and logs to both tables as needed.
func logToolAccess(db *sql.DB, sessionID, toolName string, params map[string]any) {
	file, function := extractAccessInfo(toolName, params)
	if file == "" {
		return
	}
	language := languageForFile(file)
	if err := logFileAccess(db, sessionID, file, toolName, language, params); err != nil {
		LogError("Failed to log file access: %v", err)
	}
	if function != "" {
		if err := logFunctionAccess(db, sessionID, file, function, toolName, params); err != nil {
			LogError("Failed to log function access: %v", err)
		}
	}
}

// extractAccessInfo parses tool name and input parameters to extract file path
// and function/symbol information. Returns empty strings for tools that don't
// access files.
func extractAccessInfo(toolName string, input map[string]any) (file string, function string) {
	switch toolName {
	case "read_file", "read_minified_file", "write_file", "write_minified_file", "edit_file", "edit_minified_file", "delete_file":
		if path, ok := input["path"].(string); ok {
			file = path
		}

	case "lsp_query":
		if filePath, ok := input["file"].(string); ok {
			file = filePath
		}

		operation, _ := input["operation"].(string)
		switch operation {
		case "go_to_definition", "find_references", "hover", "find_implementations":
			line, _ := input["line"].(float64)
			char, _ := input["character"].(float64)
			function = fmt.Sprintf("L%d:C%d", int(line), int(char))
		case "workspace_symbols":
			if query, ok := input["query"].(string); ok {
				function = query
			}
		}
	}

	return file, function
}

// getTopAccessedFiles returns the top N most accessed files by access count.
func getTopAccessedFiles(db *sql.DB, n int) ([]string, error) {
	rows, err := db.Query(
		"SELECT path, COUNT(*) as count FROM file_access GROUP BY path ORDER BY count DESC LIMIT ?",
		n,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []string
	for rows.Next() {
		var path string
		var count int
		if err := rows.Scan(&path, &count); err != nil {
			return nil, err
		}
		files = append(files, path)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return files, nil
}
