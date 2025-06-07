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
	db, err = sql.Open("sqlite3", "data.db")
	if err != nil {
		log.Fatal(err)
	}
	_, _ = db.Exec(`
	CREATE TABLE IF NOT EXISTS config (
		key TEXT PRIMARY KEY,
		value TEXT
	);
	CREATE TABLE IF NOT EXISTS sensors (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
	sensor_name TEXT UNIQUE,
	min_value REAL,
	max_value REAL
	);
	`)
}

func Index(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "templates/index.html")
}

func setSetting(key, value string) {
	_, _ = db.Exec("INSERT OR REPLACE INTO config (key, value) VALUES (?, ?)", key, value)
}

func getSetting(key string) string {
	row := db.QueryRow("SELECT value FROM config WHERE key = ?", key)
	var value string
	_ = row.Scan(&value)
	return value
}

func updateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Некорректный метод", http.StatusMethodNotAllowed)
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
	w.Write([]byte("Реквизиты доступа обновлены"))
}

func addSensorHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("sensor_name")
	minStr := r.URL.Query().Get("min_value")
	maxStr := r.URL.Query().Get("max_value")

	if name == "" {
		http.Error(w, "Добавьте имя датчика", http.StatusBadRequest)
		return
	}

	// Проверяем наличие датчика
	var exists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sensors WHERE name = ?)", name).Scan(&exists)
	if err != nil {
		http.Error(w, "DB error", http.StatusInternalServerError)
		return
	}
	if exists {
		http.Error(w, "Sensor already exists", http.StatusConflict)
		return
	}

	// Преобразуем пороги
	var minVal, maxVal sql.NullFloat64
	if v, err := strconv.ParseFloat(minStr, 64); err == nil {
		minVal = sql.NullFloat64{Float64: v, Valid: true}
	}
	if v, err := strconv.ParseFloat(maxStr, 64); err == nil {
		maxVal = sql.NullFloat64{Float64: v, Valid: true}
	}

	_, err = db.Exec("INSERT INTO sensors (name, min_value, max_value) VALUES (?, ?, ?)", name, minVal, maxVal)
	if err != nil {
		http.Error(w, "Insert failed", http.StatusInternalServerError)
		return
	}

	w.Write([]byte("Sensor added with thresholds"))
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
	rows, err := db.Query("SELECT id, name, min_value, max_value FROM sensors")
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var name string
		var min_value sql.NullFloat64
		var max_value sql.NullFloat64
		_ = rows.Scan(&id, &name, &min_value, &max_value)

		minStr := "not set"
		maxStr := "not set"
		if min_value.Valid {
			minStr = fmt.Sprintf("%.2f", min_value.Float64)
		}
		if max_value.Valid {
			maxStr = fmt.Sprintf("%.2f", max_value.Float64)
		}

		fmt.Fprintf(w, "%d: %s | min_value: %s | max_value: %s\n", id, name, minStr, maxStr)
	}
}

func updateThresholdHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		return
	}

	type ThresholdInput struct {
		Name string   `json:"name"`      // имя датчика
		Min  *float64 `json:"min_value"` // порог минимум, опционально
		Max  *float64 `json:"max_value"` // порог максимум, опционально
	}

	var input ThresholdInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if input.Name == "" {
		http.Error(w, "Missing sensor name", http.StatusBadRequest)
		return
	}

	if input.Min != nil {
		_, err := db.Exec("UPDATE sensors SET min = ? WHERE name = ?", *input.Min, input.Name)
		if err != nil {
			http.Error(w, "DB error (min)", http.StatusInternalServerError)
			return
		}
	}

	if input.Max != nil {
		_, err := db.Exec("UPDATE sensors SET max_value = ? WHERE name = ?", *input.Max, input.Name)
		if err != nil {
			http.Error(w, "DB error (max_value)", http.StatusInternalServerError)
			return
		}
	}

	w.Write([]byte("Thresholds updated"))
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
	http.HandleFunc("/update_threshold", updateThresholdHandler)
	http.HandleFunc("/test", testHandler)
	http.HandleFunc("/fetch_sensors", fetchSensorsHandler)
	log.Println("Server started on :8080")
	http.ListenAndServe(":8080", nil)
}
