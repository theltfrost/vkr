package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var db *sql.DB

func initDB() {
	var err error
	db, err = sql.Open("sqlite3", "sensor.db")
	if err != nil {
		log.Fatal(err)
	}
	_, _ = db.Exec(`
	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT
	);
	CREATE TABLE IF NOT EXISTS sensors (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT UNIQUE
	);
	`)
}

func setSetting(key, value string) {
	_, _ = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", key, value)
}

func getSetting(key string) string {
	row := db.QueryRow("SELECT value FROM settings WHERE key = ?", key)
	var value string
	_ = row.Scan(&value)
	return value
}

func updateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		return
	}
	type Input struct {
		HAURL    string `json:"ha_url"`
		HAToken  string `json:"ha_token"`
		TGToken  string `json:"tg_token"`
		CronTime string `json:"cron_interval"`
	}
	var input Input
	_ = json.NewDecoder(r.Body).Decode(&input)
	if input.HAURL != "" {
		setSetting("ha_url", input.HAURL)
	}
	if input.HAToken != "" {
		setSetting("ha_token", input.HAToken)
	}
	if input.TGToken != "" {
		setSetting("tg_token", input.TGToken)
	}
	if input.CronTime != "" {
		setSetting("cron_interval", input.CronTime)
	}
	w.Write([]byte("Updated"))
}

func addSensorHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Missing sensor ID", http.StatusBadRequest)
		return
	}
	_, _ = db.Exec("INSERT INTO sensors (name) VALUES (?)", id)
	w.Write([]byte("Sensor added"))
}

func deleteSensorHandler(w http.ResponseWriter, r *http.Request) {
	numStr := r.URL.Query().Get("num")
	idx, err := strconv.Atoi(numStr)
	if err != nil {
		http.Error(w, "Invalid number", http.StatusBadRequest)
		return
	}
	_, _ = db.Exec("DELETE FROM sensors WHERE id = ?", idx)
	w.Write([]byte("Sensor deleted"))
}

func listSensorsHandler(w http.ResponseWriter, r *http.Request) {
	rows, _ := db.Query("SELECT id, name FROM sensors")
	defer rows.Close()
	for rows.Next() {
		var id int
		var name string
		_ = rows.Scan(&id, &name)
		fmt.Fprintf(w, "%d: %s\n", id, name)
	}
}

func testHandler(w http.ResponseWriter, r *http.Request) {
	go diagnose()
	w.Write([]byte("Test started"))
}

func diagnose() {
	haURL := getSetting("ha_url")
	haToken := getSetting("ha_token")
	tgToken := getSetting("tg_token")

	rows, _ := db.Query("SELECT name FROM sensors")
	defer rows.Close()

	for rows.Next() {
		var sensor string
		_ = rows.Scan(&sensor)
		url := fmt.Sprintf("%s/api/states/%s", haURL, sensor)
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+haToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			notifyTelegram(tgToken, fmt.Sprintf("Sensor %s not available", sensor))
			continue
		}
		var data struct {
			LastChanged time.Time `json:"last_changed"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&data)
		dur := time.Since(data.LastChanged)
		if dur.Hours() < 1 {
			notifyTelegram(tgToken, fmt.Sprintf("Sensor %s last seen %.1f hours ago", sensor, dur.Hours()))
		}
	}
}

func notifyTelegram(token, message string) {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]string{
		"chat_id": "176418396", // заменить на ID пользователя или канала
		"text":    message,
	}
	body, _ := json.Marshal(payload)
	http.Post(url, "application/json", bytes.NewReader(body))
}

func Index(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "templates/index.html")
}

func fetchSensorsHandler(w http.ResponseWriter, r *http.Request) {
	go fetchAndSaveAllSensors()
	w.Write([]byte("Sensor fetch initiated"))
}

func fetchAndSaveAllSensors() {
	haURL := getSetting("ha_url")
	haToken := getSetting("ha_token")

	req, err := http.NewRequest("GET", haURL+"/api/states", nil)
	if err != nil {
		log.Println("Error creating request:", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+haToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Println("Error fetching states:", err)
		return
	}
	defer resp.Body.Close()

	var result []struct {
		EntityID string `json:"entity_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Println("Error decoding JSON:", err)
		return
	}

	for _, item := range result {
		if strings.HasPrefix(item.EntityID, "sensor.") {
			_, err := db.Exec("INSERT OR IGNORE INTO sensors (name) VALUES (?)", item.EntityID)
			if err != nil {
				log.Println("Error inserting sensor:", err)
			}
		}
	}
}

func main() {
	initDB()
	http.HandleFunc("/", Index)
	http.HandleFunc("/update", updateHandler)
	http.HandleFunc("/add_sensor", addSensorHandler)
	http.HandleFunc("/delete_sensor", deleteSensorHandler)
	http.HandleFunc("/list_sensors", listSensorsHandler)
	http.HandleFunc("/test", testHandler)
	http.HandleFunc("/fetch_sensors", fetchSensorsHandler)
	log.Println("Server started on :8080")
	http.ListenAndServe(":8080", nil)
}
