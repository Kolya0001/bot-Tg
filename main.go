package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var (
	bot    *tgbotapi.BotAPI
	logger = log.New(os.Stdout, "BOT: ", log.LstdFlags|log.Lshortfile)
	// Используем sync.Map для хранения прогресса
	progressCache sync.Map
)

type Config struct {
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

	// Инициализация бота
	if bot, err = tgbotapi.NewBotAPI(cfg.BotToken); err != nil {
		logger.Panic("Ошибка инициализации бота:", err)
	}
	logger.Printf("Авторизован как %s", bot.Self.UserName)

	// Запуск обработчика обновлений
	go processUpdates(tgbotapi.NewUpdate(0))

	// Устанавливаем переменную окружения PORT, если она не установлена
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // Значение по умолчанию
	}

	// Запускаем фоновый HTTP сервер, чтобы Render не ругался на отсутствие открытых портов
	go func() {
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			logger.Printf("Ошибка при запуске HTTP сервера: %v", err)
		}
	}()

	// Бот будет продолжать работать, не блокируя выполнение основной программы
	select {} // Блокировка main, чтобы приложение не завершилось
}

func loadConfig() (*Config, error) {
	var cfg Config
	cfg.BotToken = os.Getenv("TELEGRAM_TOKEN") // Получаем токен из переменной окружения

	if cfg.BotToken == "" {
		return nil, fmt.Errorf("TELEGRAM_TOKEN не установлен")
	}

	return &cfg, nil
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

	// Если нет кэша, создаем новый
	progress := make(map[int]bool)

	// Сохраняем прогресс в кэш
	progressCache.Store(userID, progress)

	return progress, nil
}

func saveUserProgress(userID int64, taskID int, solved bool) error {
	// Извлекаем текущий прогресс из кэша
	progress, _ := getUserProgress(userID)

	// Обновляем прогресс
	progress[taskID] = solved

	// Сохраняем обновленный прогресс в кэш
	progressCache.Store(userID, progress)

	return nil
}

func sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, err := bot.Send(msg); err != nil {
		logger.Printf("Ошибка отправки сообщения: %v", err)
	}
}
