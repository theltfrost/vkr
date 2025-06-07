package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

var db *sql.DB
var lastErrorHash string

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
		ChatID   string `json:"chat_id"`
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
	if input.ChatID != "" {
		setSetting("chat_id", input.ChatID)
	}
	if input.CronTime != "" {
		setSetting("cron_interval", input.CronTime)
	}
	w.Write([]byte("Реквизиты доступа обновлены"))
}

func addSensorHandler(w http.ResponseWriter, r *http.Request) {
	sensor_name := r.URL.Query().Get("sensor_name")
	minStr := r.URL.Query().Get("min_value")
	maxStr := r.URL.Query().Get("max_value")

	if sensor_name == "" {
		http.Error(w, "Добавьте имя датчика", http.StatusBadRequest)
		return
	}

	// Проверяем наличие датчика
	var exists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sensors WHERE sensor_name = ?)", sensor_name).Scan(&exists)
	if err != nil {
		http.Error(w, "Ошибка базы данных", http.StatusInternalServerError)
		return
	}
	if exists {
		http.Error(w, "Датчик уже добавлен", http.StatusConflict)
		return
	}

	// Добавляем пороговые значения
	var minVal, maxVal sql.NullFloat64
	if v, err := strconv.ParseFloat(minStr, 64); err == nil {
		minVal = sql.NullFloat64{Float64: v, Valid: true}
	}
	if v, err := strconv.ParseFloat(maxStr, 64); err == nil {
		maxVal = sql.NullFloat64{Float64: v, Valid: true}
	}

	_, err = db.Exec("INSERT INTO sensors (sensor_name, min_value, max_value) VALUES (?, ?, ?)", sensor_name, minVal, maxVal)
	if err != nil {
		http.Error(w, "Ошибка добавления", http.StatusInternalServerError)
		return
	}

	w.Write([]byte("Датчик успешно добавлен"))
}

func deleteSensorHandler(w http.ResponseWriter, r *http.Request) {
	sensor_id_str := r.URL.Query().Get("sensor_id_rm")
	id_rm, err := strconv.Atoi(sensor_id_str)
	if err != nil {
		http.Error(w, "Некорректнй ID", http.StatusBadRequest)
		return
	}
	_, _ = db.Exec("DELETE FROM sensors WHERE id = ?", id_rm)
	w.Write([]byte("Датчик успешно удален"))
}

func listSensorsHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, sensor_name, min_value, max_value FROM sensors")
	if err != nil {
		http.Error(w, "Ошибка базы данных", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		var sensor_name string
		var min_value sql.NullFloat64
		var max_value sql.NullFloat64
		_ = rows.Scan(&id, &sensor_name, &min_value, &max_value)

		minStr := "not set"
		maxStr := "not set"
		if min_value.Valid {
			minStr = fmt.Sprintf("%.2f", min_value.Float64)
		}
		if max_value.Valid {
			maxStr = fmt.Sprintf("%.2f", max_value.Float64)
		}

		fmt.Fprintf(w, "%d: %s | min_value: %s | max_value: %s\n", id, sensor_name, minStr, maxStr)
	}
}

func updateSensorThresholdsHandler(w http.ResponseWriter, r *http.Request) {
	sensorID := r.URL.Query().Get("sensor_id")
	minStr := r.URL.Query().Get("min_value")
	maxStr := r.URL.Query().Get("max_value")

	if sensorID == "" {
		http.Error(w, "ID сенсора не указан", http.StatusBadRequest)
		return
	}

	// Проверка наличия датчика
	var exists bool
	err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sensors WHERE id = ?)", sensorID).Scan(&exists)
	if err != nil {
		http.Error(w, "Ошибка при проверке наличия датчика", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "Датчик с таким ID не найден", http.StatusNotFound)
		return
	}

	// Преобразование пороговых значений
	var min_value, max_value sql.NullFloat64
	if v, err := strconv.ParseFloat(minStr, 64); err == nil {
		min_value = sql.NullFloat64{Float64: v, Valid: true}
	}
	if v, err := strconv.ParseFloat(maxStr, 64); err == nil {
		max_value = sql.NullFloat64{Float64: v, Valid: true}
	}

	// Обновление порогов
	_, err = db.Exec("UPDATE sensors SET min_value = ?, max_value = ? WHERE id = ?", min_value, max_value, sensorID)
	if err != nil {
		http.Error(w, "Ошибка при обновлении порогов", http.StatusInternalServerError)
		return
	}

	w.Write([]byte("Пороговые значения успешно обновлены"))
}

// Запуск тестирования
func testHandler(w http.ResponseWriter, r *http.Request) {
	go diagnostics()
	w.Write([]byte("Тестирование запущено"))
}

func diagnostics() {
	// Получаем настройки
	haURL := getSetting("ha_url")
	haToken := getSetting("ha_token")
	tgToken := getSetting("tg_token")

	apiCheckURL := fmt.Sprintf("%s/api/", haURL)
	reqAPI, _ := http.NewRequest("GET", apiCheckURL, nil)
	reqAPI.Header.Set("Authorization", "Bearer "+haToken)
	logURL := fmt.Sprintf("%s/api/error_log", haURL)
	reqLog, _ := http.NewRequest("GET", logURL, nil)
	respLog, err := http.DefaultClient.Do(reqLog)
	reqLog.Header.Set("Authorization", "Bearer "+haToken)
	reqLog.Header.Set("Authorization", "Bearer "+haToken)

	respAPI, err := http.DefaultClient.Do(reqAPI)
	if err != nil || respAPI.StatusCode != 200 {
		notifyTelegram(tgToken, "API Home Assistant недоступна (ошибка соединения)")
	} else {
		defer respAPI.Body.Close()
		var apiStatus struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(respAPI.Body).Decode(&apiStatus); err != nil || apiStatus.Message != "API running." {
			notifyTelegram(tgToken, "API Home Assistant недоступна или отвечает некорректно")
		}
	}

	if err == nil && respLog.StatusCode == 200 {
		defer respLog.Body.Close()
		body, _ := io.ReadAll(respLog.Body)

		if bytes.Contains(body, []byte(" ERROR ")) {
			hash := fmt.Sprintf("%x", sha256.Sum256(body))
			if hash != lastErrorHash {
				lastErrorHash = hash
				notifyTelegram(tgToken, fmt.Sprintf("Ошибки в журнале Home Assistant:\n\n%s", string(body)))
			}
		}
	}

	// Получаем список датчиков с порогами
	rows, err := db.Query("SELECT sensor_name, min_value, max_value FROM sensors")
	if err != nil {
		log.Printf("Ошибка при запросе датчиков: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var sensor string
		var minVal, maxVal sql.NullFloat64
		_ = rows.Scan(&sensor, &minVal, &maxVal)

		// Запрашиваем состояние датчика
		url := fmt.Sprintf("%s/api/states/%s", haURL, sensor)
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+haToken)
		resp, err := http.DefaultClient.Do(req)

		if err != nil || resp.StatusCode != 200 {
			notifyTelegram(tgToken, fmt.Sprintf("Датчик %s не отвечает", sensor))
			continue
		}

		// Обрабатываем ответ
		var state struct {
			State       string         `json:"state"`
			LastChanged time.Time      `json:"last_changed"`
			Attributes  map[string]any `json:"attributes"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&state)
		resp.Body.Close()

		// Проверка: измеряет ли датчик значения
		if sc, ok := state.Attributes["state_class"]; !ok || sc != "measurement" {
			continue
		}

		// Обработка батарейных датчиков
		if dc, ok := state.Attributes["device_class"]; ok && dc == "battery" {
			valFloat, err := strconv.ParseFloat(state.State, 64)
			if err == nil {
				if valFloat > 5 {
					continue
				}
				notifyTelegram(tgToken, fmt.Sprintf("Батарея датчика %s разряжается (%.0f%%)", sensor, valFloat))
				continue
			}
		}

		// Проверка давности обновления
		dur := time.Since(state.LastChanged)
		if dur.Hours() >= 6 {
			notifyTelegram(tgToken, fmt.Sprintf("Датчик %s: значения не менялись %.1f часов", sensor, dur.Hours()))
		}

		// Проверка порогов, если заданы
		if minVal.Valid || maxVal.Valid {
			value, err := strconv.ParseFloat(state.State, 64)
			if err != nil {
				continue
			}
			if (minVal.Valid && value < minVal.Float64) || (maxVal.Valid && value > maxVal.Float64) {
				notifyTelegram(tgToken, fmt.Sprintf("Датчик %s: значение %.2f вне порогов", sensor, value))
			}
		}
	}
}

func notifyTelegram(token, message string) {
	chatID := getSetting("chat_id")
	if chatID == "" {
		log.Println("Chat ID не задан")
		return
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	payload := map[string]string{
		"chat_id": chatID,
		"text":    message,
	}
	body, _ := json.Marshal(payload)

	_, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("Ошибка отправки сообщения в Telegram: %v", err)
	}
}

func fetchSensorsHandler(w http.ResponseWriter, r *http.Request) {
	go fetchAllSensors()
	w.Write([]byte("Запущено обновление датчиков, обновите список"))
}

func fetchAllSensors() {
	haURL := getSetting("ha_url")
	haToken := getSetting("ha_token")

	req, err := http.NewRequest("GET", haURL+"/api/states", nil)
	if err != nil {
		log.Println("Ошибка запроса:", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+haToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Println("Ошибка получения иформации о датчике:", err)
		return
	}
	defer resp.Body.Close()

	var result []struct {
		EntityID string `json:"entity_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Println("Ошибка декодирования JSON:", err)
		return
	}

	for _, item := range result {
		if strings.HasPrefix(item.EntityID, "sensor.") {
			_, err := db.Exec("INSERT OR IGNORE INTO sensors (sensor_name) VALUES (?)", item.EntityID)
			if err != nil {
				log.Println("Ошибка добавления датчика:", err)
			}
		}
	}
}

func startDiagnosticsTicker() {
	intervalStr := getSetting("cron_interval")
	minutes, err := strconv.Atoi(intervalStr)
	if err != nil || minutes <= 0 {
		log.Printf("Некорректное значение интервала диагностики: %s", intervalStr)
		return
	}

	duration := time.Duration(minutes) * time.Minute
	log.Printf("Диагностика будет запускаться каждые %d минут", minutes)

	go func() {
		ticker := time.NewTicker(duration)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				log.Println("Запуск диагностики по расписанию")
				diagnostics()
			}
		}
	}()
}

func main() {
	initDB()
	startDiagnosticsTicker()
	http.HandleFunc("/", Index)
	http.HandleFunc("/update", updateHandler)
	http.HandleFunc("/add_sensor", addSensorHandler)
	http.HandleFunc("/delete_sensor", deleteSensorHandler)
	http.HandleFunc("/list_sensors", listSensorsHandler)
	http.HandleFunc("/update_thresholds", updateSensorThresholdsHandler)
	http.HandleFunc("/test", testHandler)
	http.HandleFunc("/fetch_sensors", fetchSensorsHandler)
	log.Println("Сервер запущен на порту :8080")
	http.ListenAndServe(":8080", nil)
}
