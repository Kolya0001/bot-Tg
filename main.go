package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

var (
	db            *sql.DB
	bot           *tgbotapi.BotAPI
	logger        = log.New(os.Stdout, "BOT: ", log.LstdFlags|log.Lshortfile)
	progressCache sync.Map
)

type Config struct {
	DB struct {
		Host     string
		Port     string
		Name     string
		User     string
		Password string
	}
	BotToken string
}

type Task struct {
	ID      int
	Text    string
	Answer  string
	Options []string
}

func main() {
	// Инициализация конфигурации
	cfg, err := loadConfig()
	if err != nil {
		logger.Fatal("Ошибка загрузки конфигурации:", err)
	}

	// Подключение к БД
	if err = initDB(cfg); err != nil {
		logger.Fatal("Ошибка инициализации БД:", err)
	}
	defer db.Close()

	// Инициализация бота
	if bot, err = tgbotapi.NewBotAPI(cfg.BotToken); err != nil {
		logger.Panic("Ошибка инициализации бота:", err)
	}
	logger.Printf("Авторизован как %s", bot.Self.UserName)

	// Запуск обработчика обновлений
	processUpdates(tgbotapi.NewUpdate(0))
}

func loadConfig() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		return nil, fmt.Errorf("ошибка загрузки .env: %v", err)
	}

	var cfg Config
	cfg.DB.Host = os.Getenv("DB_HOST")
	cfg.DB.Port = os.Getenv("DB_PORT")
	cfg.DB.Name = os.Getenv("DB_NAME")
	cfg.DB.User = os.Getenv("DB_USER")
	cfg.DB.Password = os.Getenv("DB_PASSWORD")
	cfg.BotToken = "7949936274:AAFsZMMLnb-SwGJiQUDXAa0aVd8zNWIzyOA"

	return &cfg, nil
}

func initDB(cfg *Config) error {
	connStr := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		cfg.DB.Host, cfg.DB.Port, cfg.DB.User, cfg.DB.Password, cfg.DB.Name,
	)

	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		return fmt.Errorf("ошибка подключения к БД: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err = db.PingContext(ctx); err != nil {
		return fmt.Errorf("ошибка проверки соединения: %v", err)
	}

	// Создание таблиц
	if _, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS user_progress (
			user_id BIGINT,
			task_id INTEGER,
			solved BOOLEAN,
			PRIMARY KEY (user_id, task_id)
		);
	`); err != nil {
		return fmt.Errorf("ошибка создания таблицы: %v", err)
	}

	logger.Println("База данных успешно инициализирована")
	return nil
}

func processUpdates(updateConfig tgbotapi.UpdateConfig) {
	updates := bot.GetUpdatesChan(updateConfig)

	for update := range updates {
		if update.CallbackQuery != nil {
			handleCallbackQuery(update.CallbackQuery)
			continue
		}

		if update.Message != nil && update.Message.IsCommand() {
			handleCommand(update.Message)
		}
	}
}

func handleCommand(msg *tgbotapi.Message) {
	switch msg.Command() {
	case "start":
		sendWelcome(msg.Chat.ID)
	case "task":
		handleTaskCommand(msg.Chat.ID)
	case "progress":
		showProgress(msg.Chat.ID)
	default:
		sendMessage(msg.Chat.ID, "Неизвестная команда 🤷")
	}
}

func handleTaskCommand(chatID int64) {
	task := getNextTask(chatID)
	if task == nil {
		sendMessage(chatID, "Поздравляем! Вы решили все задачи 🎉")
		return
	}
	sendTask(chatID, task)
}

func getNextTask(userID int64) *Task {
	progress, err := getUserProgress(userID)
	if err != nil {
		logger.Printf("Ошибка получения прогресса: %v", err)
		return nil
	}

	tasks := getTasks()
	for i := range tasks {
		if solved, exists := progress[tasks[i].ID]; !exists || !solved {
			return &tasks[i]
		}
	}
	return nil
}

func sendTask(chatID int64, task *Task) {
	var buttons []tgbotapi.InlineKeyboardButton
	for _, option := range task.Options {
		callbackData := fmt.Sprintf("%d:%s", task.ID, option)
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData(option, callbackData))
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(buttons)
	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("%s\n\nВыберите правильный ответ:", task.Text))
	msg.ReplyMarkup = keyboard

	if _, err := bot.Send(msg); err != nil {
		logger.Printf("Ошибка отправки задачи: %v", err)
	}
}

func handleCallbackQuery(query *tgbotapi.CallbackQuery) {
	parts := strings.SplitN(query.Data, ":", 2)
	if len(parts) != 2 {
		logger.Printf("Некорректный callback: %s", query.Data)
		return
	}

	taskID, err := strconv.Atoi(parts[0])
	if err != nil {
		logger.Printf("Ошибка парсинга taskID: %v", err)
		return
	}

	task := findTask(taskID)
	if task == nil {
		logger.Printf("Задача %d не найдена", taskID)
		return
	}

	userID := query.From.ID
	answerCorrect := parts[1] == task.Answer

	// Обновление прогресса
	if answerCorrect {
		if err := saveUserProgress(int64(userID), task.ID, true); err != nil {
			logger.Printf("Ошибка сохранения прогресса: %v", err)
		}
	}

	// Отправка ответа
	callbackCfg := tgbotapi.NewCallback(query.ID, "")
	if answerCorrect {
		callbackCfg.Text = "Правильно! ✅"
	} else {
		callbackCfg.Text = "Неверно ❌ Попробуйте еще раз!"
	}

	if _, err := bot.Request(callbackCfg); err != nil {
		logger.Printf("Ошибка обработки callback: %v", err)
	}

	// Обновление сообщения или отправка следующей задачи
	if answerCorrect {
		if nextTask := getNextTask(int64(userID)); nextTask != nil {
			sendTask(int64(userID), nextTask)
		} else {
			sendMessage(int64(userID), "🎉 Вы решили все доступные задачи!")
		}
	}
}

func findTask(taskID int) *Task {
	for i := range getTasks() {
		if getTasks()[i].ID == taskID {
			return &getTasks()[i]
		}
	}
	return nil
}

func getTasks() []Task {
	return []Task{
		{ID: 1, Text: "Какой оператор используется для объявления переменной в Go?", Answer: "var", Options: []string{"let", "const", "var", "define"}},
		{ID: 2, Text: "Какой тип данных используется для целых чисел в Go?", Answer: "int", Options: []string{"integer", "float", "int", "number"}},
		{ID: 3, Text: "Какой тип данных используется для строк в Go?", Answer: "string", Options: []string{"char", "string", "text", "varchar"}},
		{ID: 4, Text: "Какая директива используется для импорта пакетов в Go?", Answer: "import", Options: []string{"include", "import", "use", "require"}},
		{ID: 5, Text: "Что выводит команда fmt.Println(1+1) в Go?", Answer: "2", Options: []string{"1", "2", "3", "Ошибка"}},
	}
}

func sendWelcome(chatID int64) {
	text := `Добро пожаловать в бота для изучения Go! 🚀

Используйте команды:
/task - Получить новую задачу
/progress - Показать прогресс`
	sendMessage(chatID, text)
}

func showProgress(chatID int64) {
	progress, err := getUserProgress(chatID)
	if err != nil {
		logger.Printf("Ошибка получения прогресса: %v", err)
		sendMessage(chatID, "Ошибка получения прогресса 😕")
		return
	}

	total := len(getTasks())
	solved := 0
	for _, v := range progress {
		if v {
			solved++
		}
	}

	text := fmt.Sprintf("Ваш прогресс: 📊\n\nРешено задач: %d/%d\nПрогресс: %.1f%%",
		solved, total, float64(solved)/float64(total)*100)
	sendMessage(chatID, text)
}

func getUserProgress(userID int64) (map[int]bool, error) {
	// Проверка кэша
	if cached, ok := progressCache.Load(userID); ok {
		return cached.(map[int]bool), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	rows, err := db.QueryContext(ctx,
		"SELECT task_id, solved FROM user_progress WHERE user_id = $1", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	progress := make(map[int]bool)
	for rows.Next() {
		var taskID int
		var solved bool
		if err := rows.Scan(&taskID, &solved); err != nil {
			return nil, err
		}
		progress[taskID] = solved
	}

	// Обновление кэша
	progressCache.Store(userID, progress)
	return progress, nil
}

func saveUserProgress(userID int64, taskID int, solved bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := db.ExecContext(ctx,
		`INSERT INTO user_progress (user_id, task_id, solved)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, task_id)
		DO UPDATE SET solved = $3`,
		userID, taskID, solved,
	)

	// Сброс кэша при обновлении
	progressCache.Delete(userID)
	return err
}

func sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := bot.Send(msg); err != nil {
		logger.Printf("Ошибка отправки сообщения: %v", err)
	}
}
