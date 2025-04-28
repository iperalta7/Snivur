package db

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3" // Import SQLite driver
)

var db *sql.DB

// InitDB initializes the database connection
func InitDB() error {
	var err error
	// Open SQLite database
	db, err = sql.Open("sqlite3", "./servers.db")
	if err != nil {
		return fmt.Errorf("failed to open database: %v", err)
	}

	// Create table if it doesn't exist
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS servers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			status TEXT NOT NULL,
			path TEXT NOT NULL
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create table: %v", err)
	}

	return nil
}

// AddServer adds a new server to the database
func AddServer(name, status, path string) error {
	// Prepare SQL statement
	stmt, err := db.Prepare("INSERT INTO servers(name, status, path) VALUES (?, ?, ?)")
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %v", err)
	}
	defer stmt.Close()

	// Execute the SQL statement
	_, err = stmt.Exec(name, status, path)
	if err != nil {
		return fmt.Errorf("failed to execute statement: %v", err)
	}

	return nil
}

// UpdateServerStatus updates the status of a server
func UpdateServerStatus(name, status string) error {
	// Prepare SQL statement
	stmt, err := db.Prepare("UPDATE servers SET status = ? WHERE name = ?")
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %v", err)
	}
	defer stmt.Close()

	// Execute the SQL statement
	_, err = stmt.Exec(status, name)
	if err != nil {
		return fmt.Errorf("failed to execute statement: %v", err)
	}

	return nil
}

// GetServers fetches all the servers from the database
func GetServers() ([]Server, error) {
    rows, err := db.Query("SELECT name, status, path FROM servers")
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var servers []Server
    for rows.Next() {
        var server Server
        if err := rows.Scan(&server.Name, &server.Status, &server.Path); err != nil {
            return nil, err
        }
        servers = append(servers, server)
    }

    return servers, nil
}

// Server struct to represent a server in the database
type Server struct {
    Name   string
    Status string
    Path   string
}
