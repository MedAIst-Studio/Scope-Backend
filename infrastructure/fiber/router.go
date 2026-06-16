//	@title		MedAIist-Backend API
//	@version	1.0
//	@description
//	@description	## How to use the API
//	@description	1- Click which endpoint you want to use.
//	@description	2- Click "Try it out" button.
//	@description	3- Add your request body or your parameters which are showed and required by the endpoint.
//	@description	4- Click "Execute" button.
//	@description	5- You will see the response.
//	@description
//	@description	Some endpoints require authentication. In this case, you need to log in first.
//	@description	I created two types of users for this project: admin and regular user.
//	@description	Just send a POST request as below at [here](http://localhost:3000/swagger/index.html/).
//	@description	After login, you will get a JWE token in cookies.
//	@description	If you're using cookie-based auth, the cookie will be sent automatically.
//	@description	Alternatively, you can use Bearer Token authentication via the "Authorize" button.
//	@description
//	@description	### Login Request For Admin
//	@description	```json
//	@description	{
//	@description	"email": "admin@admin.com",
//	@description	"password": "admin123"
//	@description	}
//	@description	```
//	@description
//	@description	### Login Request For User
//	@description	```json
//	@description	{
//	@description	"email": "user@user.com",
//	@description	"password": "user1234"
//	@description	}
//	@description	```
//	@description
//	@description	## Error Handling
//	@description	All error responses will follow this JSON format:
//	@description
//	@description	```json
//	@description	{
//	@description	"message": string,
//	@description	"code": int
//	@description	}
//	@description	```
//	@description	### Example
//	@description	```json
//	@description	{
//	@description	"message": "invalid request",
//	@description	"code": 400
//	@description	}
//	@description	```
//	@description
//	@description	## Reminder
//	@description	I did not use `/api` prefix for the endpoint routes. Because I love to host my API on "api" subdomain.
//	@description	Status code with `2xx` is a success code.
//	@description	Status code with `4xx` is a client error code.
//	@description	Status code with `5xx` is a server error code.
//

// @securityDefinitions.apikey	BearerAuth
// @in							header
// @name						Authorization
// @description				Enter your Bearer token in the format **Bearer &lt;token&gt;**
//
// @host						localhost:3000
package fiber

import (
	"context"
	"fmt"
	"net/http"

	"github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/oauth"
	"github.com/MedAIst-Studio/MedAIist-Backend/jobs"
	"github.com/MedAIst-Studio/MedAIist-Backend/services"

	"github.com/MedAIst-Studio/MedAIist-Backend/domain"
	"github.com/MedAIst-Studio/MedAIist-Backend/handlers/auth"
	"github.com/MedAIst-Studio/MedAIist-Backend/handlers/chat"
	"github.com/MedAIst-Studio/MedAIist-Backend/handlers/file"
	"github.com/MedAIst-Studio/MedAIist-Backend/handlers/healthcheck"
	"github.com/MedAIst-Studio/MedAIist-Backend/handlers/message"
	"github.com/MedAIst-Studio/MedAIist-Backend/handlers/quiz"
	subscriptionHandler "github.com/MedAIst-Studio/MedAIist-Backend/handlers/subscription"
	"github.com/MedAIst-Studio/MedAIist-Backend/handlers/system"
	"github.com/MedAIst-Studio/MedAIist-Backend/handlers/user"

	fiberSwagger "github.com/swaggo/fiber-swagger"

	"github.com/gofiber/fiber/v2"
)

type handlerAdapter[R Request, Res Response] struct {
	f func(context.Context, *R) (*Res, int, error)
}

func (h handlerAdapter[R, Res]) Handle(ctx context.Context, req *R) (*Res, int, error) {
	return h.f(ctx, req)
}

func SetupRoutes(app *fiber.App) {
	dc := NewAuthDependencyContainer()

	middlewareManager := NewMiddlewareManager(dc.jweTokenService, dc.slogLogger, dc.SubscriptionService)

	healthcheckHandler := healthcheck.NewHealthcheckHandler()
	systemConfigHandler := system.NewSystemConfigHandler()

	if !domain.IsProdEnv() {
		domain.CookieSecure = false
	}

	authService := services.NewAuthService(dc.Repo, dc.jweTokenService, dc.mailerSender, dc.inMemoryClient)
	authHandler := auth.NewAuthHandler(&auth.AuthConfig{
		AuthService: authService,
		Validator:   dc.Validator,
		Logger:      dc.slogLogger,
	})

	googleOAuthService := NewGoogleOAuthService(dc)
	facebookOAuthService := NewFacebookOAuthService(dc)

	oauthService := oauth.NewOAuthService(dc.Repo, dc.jweTokenService)
	verifiers := map[string]oauth.TokenVerifier{
		"google":   oauth.NewGoogleVerifier(),
		"facebook": oauth.NewFacebookVerifier(),
	}
	mobileOAuthHandler := oauth.NewMobileOAuthHandler(oauthService, verifiers, dc.slogLogger)

	streakService := services.NewStreakService(dc.Repo)

	userService := services.NewUserService(dc.Repo, dc.FileStorage, dc.VectorDB, dc.mailerSender, dc.jweTokenService, dc.slogLogger)
	userHandler := user.NewUserHandler(userService, streakService, dc.Validator)

	chatService := services.NewChatService(dc.Repo)
	chatHandler := chat.NewChatHandler(&chat.ChatConfig{
		ChatService: chatService,
	})

	textProcessor := services.NewTextProcessor(dc.Repo, dc.VectorDB, dc.Embedding, dc.Chunker, 48)

	fileService := services.NewFileService(
		dc.Repo,
		dc.FileStorage,
		dc.VectorDB,
		streakService,
		services.NewFileProcessorFactory(
			services.NewPdfProcessor(dc.Repo, dc.FileStorage, dc.VectorDB, dc.Embedding, dc.Chunker, 48, true),
			services.NewImageProcessor(),
			textProcessor,
			textProcessor,
		),
		dc.Embedding, // OpenRouter implements ContentExtractor
	)

	fileHandler := file.NewFileHandler(&file.FileConfig{
		FileService: fileService,
	})

	messageService := services.NewMessageService(
		5,
		false,
		dc.Repo,
		dc.VectorDB,
		dc.Embedding,
		dc.AIService,
		dc.FileStorage,
		streakService,
	)
	messageHandler := message.NewMessageHandler(&message.MessageConfig{
		MessageService: messageService,
	})

	quizService := services.NewQuizService(dc.AIService, dc.Repo, dc.VectorDB, streakService)
	quizHandler := quiz.NewQuizHandler(&quiz.QuizConfig{
		QuizService: quizService,
	})

	sl := dc.slogLogger

	app.Get("/healthcheck", Handle(healthcheckHandler, sl))
	app.Get("/system/config", Handle(systemConfigHandler, sl))
	app.Use(contextMiddleware)

	authApp := app.Group("/auth")
	authApp.Post("/signup", Handle(handlerAdapter[auth.SignupRequest, auth.SignupResponse]{authHandler.HandleSignup}, sl))
	authApp.Post("/login", Handle(handlerAdapter[auth.LoginRequest, auth.LoginResponse]{authHandler.HandleLogin}, sl))
	authApp.Post("/logout", Handle(handlerAdapter[auth.LogoutRequest, auth.LogoutResponse]{authHandler.HandleLogout}, sl))
	authApp.Post("/refresh", Handle(handlerAdapter[auth.RefreshTokenRequest, auth.RefreshTokenResponse]{authHandler.HandleRefreshToken}, sl))
	authApp.Post("/exchange", Handle(handlerAdapter[auth.ExchangeRequest, auth.ExchangeResponse]{authHandler.HandleExchange}, sl))
	authApp.Get("/google", googleOAuthService.RedirectHandler)
	authApp.Get("/facebook", facebookOAuthService.RedirectHandler)

	app.Get("/api/auth/google/callback", googleOAuthService.Callback)
	app.Get("/api/auth/facebook/callback", facebookOAuthService.Callback)

	app.Post("/api/auth/oauth/mobile", mobileOAuthHandler.Handle)

	usersPublicApp := app.Group("/users")
	usersPublicApp.Post("/forgot-password", Handle(handlerAdapter[user.ForgotPasswordRequest, user.ForgotPasswordResponse]{userHandler.HandleForgotPassword}, sl))
	usersPublicApp.Post("/reset-password", Handle(handlerAdapter[user.ResetPasswordRequest, user.ResetPasswordResponse]{userHandler.HandleResetPassword}, sl))

	usersApp := app.Group("/users", middlewareManager.AuthMiddleware)
	usersApp.Get("/profile", Handle(handlerAdapter[user.GetCurrentUserRequest, user.GetCurrentUserResponse]{userHandler.HandleGetCurrentUser}, sl))
	usersApp.Delete("/account", Handle(handlerAdapter[user.DeleteAccountRequest, user.DeleteAccountResponse]{userHandler.HandleDeleteAccount}, sl))
	usersApp.Patch("/account", Handle(handlerAdapter[user.UpdateUsernameRequest, user.UpdateUsernameResponse]{userHandler.HandleUpdateUsername}, sl))
	usersApp.Patch("/password", Handle(handlerAdapter[user.ChangePasswordRequest, user.ChangePasswordResponse]{userHandler.HandleChangePassword}, sl))
	usersApp.Patch("/preferences", Handle(handlerAdapter[user.UpdatePreferencesRequest, user.UpdatePreferencesResponse]{userHandler.HandleUpdatePreferences}, sl))
	usersApp.Patch("/fcm-token", Handle(handlerAdapter[user.UpdateFCMTokenRequest, user.UpdateFCMTokenResponse]{userHandler.HandleUpdateFCMToken}, sl))
	subscriptionH := subscriptionHandler.NewSubscriptionHandler(&subscriptionHandler.SubscriptionConfig{
		SubscriptionService: dc.SubscriptionService,
	})

	usersApp.Get("/usage-stats", Handle(handlerAdapter[user.GetStreakRequest, user.GetStreakResponse]{userHandler.HandleGetStreak}, sl))
	usersApp.Get("/usage", Handle(handlerAdapter[domain.GetUsageStatusRequest, domain.UsageStatusResponse]{subscriptionH.HandleGetUsageStatus}, sl))

	chatsApp := app.Group("/chats", middlewareManager.AuthMiddleware)
	chatsApp.Get("/", Handle(handlerAdapter[domain.GetChatsRequest, domain.GetChatsResponse]{chatHandler.HandleGetChats}, sl))
	chatsApp.Get("/:id", Handle(handlerAdapter[domain.GetChatByIDRequest, domain.GetChatByIDResponse]{chatHandler.HandleGetChatByID}, sl))
	chatsApp.Delete("/:id", Handle(handlerAdapter[domain.DeleteChatRequest, domain.DeleteChatResponse]{chatHandler.HandleDeleteChat}, sl))
	chatsApp.Patch("/:id", Handle(handlerAdapter[domain.UpdateChatDTO, domain.UpdateChatResponse]{chatHandler.HandleUpdateChat}, sl))

	filesApp := app.Group("/files", middlewareManager.AuthMiddleware)
	filesApp.Post("/upload", middlewareManager.UsageLimitMiddleware(domain.ActionFileUpload), Handle(handlerAdapter[domain.UploadFileRequest, domain.UploadFileResponse]{fileHandler.HandleUploadFile}, sl))
	filesApp.Get("/", Handle(handlerAdapter[domain.GetFilesRequest, domain.GetFilesResponse]{fileHandler.HandleGetFiles}, sl))
	filesApp.Delete("/:id", Handle(handlerAdapter[domain.DeleteFileRequest, domain.DeleteFileResponse]{fileHandler.HandleDeleteFile}, sl))
	filesApp.Get("/:id", Handle(handlerAdapter[domain.GetFileRequest, domain.GetFileResponse]{fileHandler.HandleGetFile}, sl))
	filesApp.Patch("/:id/save", Handle(handlerAdapter[domain.ToggleFileSavedRequest, domain.ToggleFileSavedResponse]{fileHandler.HandleToggleFileSaved}, sl))
	filesApp.Post("/youtube", middlewareManager.UsageLimitMiddleware(domain.ActionFileUpload), Handle(handlerAdapter[domain.SubmitYouTubeRequest, domain.SubmitYouTubeResponse]{fileHandler.HandleSubmitYouTube}, sl))
	filesApp.Post("/:id/extract-notes", middlewareManager.UsageLimitMiddleware(domain.ActionFileUpload), Handle(handlerAdapter[domain.ExtractNotesRequest, domain.ExtractNotesResponse]{fileHandler.HandleExtractNotesFromImage}, sl))
	filesApp.Put("/:id/notes", Handle(handlerAdapter[domain.UpdateNotesRequest, domain.UpdateNotesResponse]{fileHandler.HandleUpdateNotes}, sl))
	messagesApp := app.Group("/messages", middlewareManager.AuthMiddleware)
	messagesApp.Post("/", middlewareManager.MessageUsageLimitMiddleware(), Handle(handlerAdapter[domain.SendMessageRequest, domain.SendMessageResponse]{messageHandler.HandleSendMessage}, sl))

	quizApp := app.Group("/quizzes", middlewareManager.AuthMiddleware)
	quizApp.Post("/", middlewareManager.UsageLimitMiddleware(domain.ActionQuiz), Handle(handlerAdapter[domain.GenerateQuizRequest, domain.GenerateQuizResponse]{quizHandler.HandleGenerateQuiz}, sl))
	quizApp.Get("/", Handle(handlerAdapter[domain.GetQuizzesRequest, domain.GetQuizzesResponse]{quizHandler.HandleGetQuizzes}, sl))
	quizApp.Get("/attempts", Handle(handlerAdapter[domain.GetAttemptsRequest, domain.GetAttemptsResponse]{quizHandler.HandleGetAttempts}, sl))
	quizApp.Get("/attempts/:id", Handle(handlerAdapter[domain.GetAttemptByIdRequest, domain.AttemptDetailResponse]{quizHandler.HandleGetAttemptById}, sl))
	quizApp.Post("/:id/solve", Handle(handlerAdapter[domain.SolveQuizRequest, domain.SolveQuizResponse]{quizHandler.HandleSolveQuiz}, sl))
	quizApp.Get("/:id", Handle(handlerAdapter[domain.GetQuizByIdRequest, domain.GetQuizByIdResponse]{quizHandler.HandleGetQuizById}, sl))

	if !domain.IsProdEnv() {
		app.Get("/swagger/*", fiberSwagger.WrapHandler)
	}

	// -- RevenueCat Webhook --
	app.Post("/webhooks/revenuecat", subscriptionH.HandleWebhook())

	app.Get("/", func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"message": "Welcome to Scope-Backend API",
			"code":    http.StatusOK,
		})
	})

	app.Use(func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusNotFound).JSON(domain.Error{
			Message: "endpoint not found",
			Code:    http.StatusNotFound,
		})
	})

	if dc.Notification != nil {
		c, err := jobs.SetupCronJobs(dc.Repo, dc.Notification, dc.Repo, dc.FileStorage)
		if err != nil {
			fmt.Printf("Warning: Failed to setup cron jobs: %v\n", err)
		} else {
			c.Start()
			fmt.Println("Cron jobs started")

			app.Hooks().OnShutdown(func() error {
				fmt.Println("Shutting down cron jobs...")
				c.Stop()
				return nil
			})
		}
	} else {
		fmt.Println("Warning: Notification service is nil, skipping cron jobs startup.")
	}
}
