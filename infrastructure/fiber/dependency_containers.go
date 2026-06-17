package fiber

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/MedAIst-Studio/MedAIist-Backend/domain"
	chunkerInfra "github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/chunker"
	firebaseInfra "github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/firebase"
	"github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/jina"
	"github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/openrouter"
	whisperInfra "github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/whisper"
	"github.com/MedAIst-Studio/MedAIist-Backend/services"

	inMemoryInfra "github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/in-memory"
	jwe "github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/jwe"
	mailersendInfra "github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/mailersend"
	s3Infra "github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/s3"

	postgresInfra "github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/postgres"
	qdrantInfra "github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/qdrant"
	slogInfra "github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/slog"
	validatorInfra "github.com/MedAIst-Studio/MedAIist-Backend/infrastructure/validator"
	"github.com/MedAIst-Studio/MedAIist-Backend/repository"
)

type DependencyContainer struct {
	Repo                *repository.Repository
	Chunker             *chunkerInfra.PdfChunkingService
	Embedding           *openrouter.OpenRouterService
	VectorDB            *qdrantInfra.QdrantVectorDB
	FileStorage         *s3Infra.S3Service
	jweTokenService     *jwe.Service
	inMemoryClient      *inMemoryInfra.InMemoryClient
	slogLogger          *slogInfra.SlogLogger
	Validator           *validatorInfra.Validator
	mailerSender        *mailersendInfra.MailerSendService
	AIService           *MultiModelService
	Whisper             domain.AudioTranscriber
	Notification        *services.NotificationService
	SubscriptionService *services.SubscriptionService
}

func NewAuthDependencyContainer() *DependencyContainer {
	postgresDB := postgresInfra.NewDB(os.Getenv("DATABASE_URL"))
	repo := repository.NewRepository(postgresDB)
	fmt.Println("Connected to database")

	jweTokenService := jwe.NewJWETokenService(&jwe.Config{
		AccessTokenEncryptionKey:  os.Getenv("ACCESS_TOKEN_ENCRYPTION_KEY"),
		RefreshTokenEncryptionKey: os.Getenv("REFRESH_TOKEN_ENCRYPTION_KEY"),
		SecureEmailEncryptionKey:  os.Getenv("SECURE_EMAIL_ENCRYPTION_KEY"),
		AuthAccessTokenDuration:   time.Minute * 15,
		AuthRefreshTokenDuration:  time.Hour * 24 * 30,
		SecureEmailTokenDuration:  time.Minute * 11,
	})

	mailersendService := mailersendInfra.NewMailerSendService(os.Getenv("MAILERSEND_API_KEY"), os.Getenv("MAILERSEND_SENDER_EMAIL"), os.Getenv("MAILERSEND_SENDER_NAME"))

	s3Service := s3Infra.NewS3Service(&s3Infra.S3Config{
		Endpoint:        os.Getenv("S3_ENDPOINT"),
		AccessKeyID:     os.Getenv("S3_ACCESS_KEY"),
		SecretAccessKey: os.Getenv("S3_SECRET_KEY"),
		UseSSL:          os.Getenv("S3_USE_SSL") == "true",
		BucketName:      os.Getenv("S3_BUCKET_NAME"),
		Location:        os.Getenv("S3_REGION"),
		PublicDomain:    os.Getenv("S3_PUBLIC_URL"),
	})
	fmt.Println("Connected to S3 Storage at", os.Getenv("S3_ENDPOINT"))

	chunker := chunkerInfra.NewChunkingService(&domain.ChunkOptions{
		MaxChunkSize:    1024,
		OverlapSize:     128,
		PreserveContext: true,
	})

	vectorDB := qdrantInfra.NewQdrantVectorDB(qdrantInfra.Config{
		Host:           os.Getenv("QDRANT_HOST"),
		Port:           6334,
		CollectionName: os.Getenv("QDRANT_COLLECTION_NAME"),
		ApiKey:         os.Getenv("QDRANT_API_KEY"),
		MinScore:       0.3,
		VectorSize:     1536,
		ChunkLimit:     10,
	})

	openRouter := openrouter.NewOpenRouterService(openrouter.Config{
		ApiKey:                 getEnv("OPENROUTER_API_KEY", ""),
		ChatModel:              getEnv("OPENROUTER_CHAT_MODEL", "google/gemini-3-flash-preview"),
		GenerateQuestionModel:  getEnv("OPENROUTER_QUESTION_MODEL", "google/gemini-3-flash-preview"),
		ExpandQuestionModel:    getEnv("OPENROUTER_EXPAND_MODEL", "openai/gpt-4o-mini"),
		GenerateChatTitleModel: getEnv("OPENROUTER_TITLE_MODEL", "openai/gpt-4o-mini"),
		ReasoningModel:         getEnv("OPENROUTER_REASONING_MODEL", "google/gemini-3-flash-preview"),
		VisionModel:            getEnv("OPENROUTER_VISION_MODEL", "google/gemini-3-flash-preview"),
		EmbeddingModel:         getEnv("OPENROUTER_EMBEDDING_MODEL", "openai/text-embedding-3-small"),
	})

	jina := jina.NewService(os.Getenv("JINA_API_KEY"))

	aiService := &MultiModelService{
		OpenRouterService: openRouter,
		Service:           jina,
	}

	whisperService := whisperInfra.NewWhisperService(getEnv("WHISPER_API_KEY", ""))

	slogLogger := slogInfra.NewLogger()
	validator := validatorInfra.NewValidator(slogLogger)

	inMemoryClient := inMemoryInfra.NewInMemoryClient(inMemoryInfra.InMemoryConfig{
		OAuthStateTTL:   time.Minute * 1,
		OAuthSessionTTL: time.Minute * 15,
	})

	var notifService *services.NotificationService
	fcmCtx := context.Background()

	messagingClient, err := firebaseInfra.NewFCMClient(fcmCtx)
	if err == nil {
		notifService = services.NewNotificationService(messagingClient, repo)
	} else {
		fmt.Printf("Warning: FCM Client initialization failed: %v\n", err)
	}

	// -- Subscription / Payment --
	usageLimits := domain.DefaultUsageLimitsConfig()
	subscriptionService := services.NewSubscriptionService(repo, usageLimits, slogLogger)

	return &DependencyContainer{
		Repo:                repo,
		Chunker:             chunker,
		Embedding:           openRouter,
		VectorDB:            vectorDB,
		FileStorage:         s3Service,
		jweTokenService:     jweTokenService,
		inMemoryClient:      inMemoryClient,
		Validator:           validator,
		slogLogger:          slogLogger,
		mailerSender:        mailersendService,
		AIService:           aiService,
		Whisper:             whisperService,
		Notification:        notifService,
		SubscriptionService: subscriptionService,
	}
}

type MultiModelService struct {
	*openrouter.OpenRouterService
	*jina.Service
}

func (s *MultiModelService) ExtractTextFromImage(ctx context.Context, imageUrl string) (string, error) {
	return s.OpenRouterService.ExtractTextFromImage(ctx, imageUrl)
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
