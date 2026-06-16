package postgres

import (
	"database/sql"
	"log"
	"time"

	_ "github.com/lib/pq"
)

func NewDB(databaseUrl string) *sql.DB {
	if databaseUrl == "" {
		panic("Database URL is empty")
	}

	db, err := sql.Open("postgres", databaseUrl)
	if err != nil {
		panic("Failed to open database: " + err.Error())
	}

	for i := 0; i < 10; i++ {
		err = db.Ping()
		if err == nil {
			break
		}
		log.Printf("Failed to connect to the database, retrying in 2 seconds... (%d/10): %v", i+1, err)
		time.Sleep(2 * time.Second)
	}

	if err != nil {
		panic("Failed to connect to the database after 10 attempts: " + err.Error())
	}

	runTableMigrations(db)

	return db
}

func runTableMigrations(db *sql.DB) {
	createTableQuery := `

-- USERS TABLE
CREATE TABLE IF NOT EXISTS  users (
    id UUID PRIMARY KEY,
    username TEXT NOT NULL,
    email TEXT NOT NULL UNIQUE,
    fcm_token TEXT DEFAULT NULL,
    password TEXT, -- encrypted
    created_at TIMESTAMP NOT NULL,
    preferences JSONB DEFAULT NULL
);

ALTER TABLE users ADD COLUMN IF NOT EXISTS preferences JSONB DEFAULT NULL;

-- USAGE STATS TABLE
CREATE TABLE IF NOT EXISTS usage_stats (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE ON UPDATE NO ACTION,
    current_streak INT DEFAULT 0,
    max_streak INT DEFAULT 0,
    last_activity_date DATE DEFAULT NULL,
    recent_active_dates JSONB DEFAULT '[]',
    UNIQUE(user_id)
);

CREATE TABLE IF NOT EXISTS oauth (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE ON UPDATE NO ACTION,
    oauth_provider TEXT NOT NULL,
    oauth_id TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE (oauth_provider, oauth_id, user_id)
);

-- FILES TABLE
CREATE TABLE IF NOT EXISTS  files (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE ON UPDATE NO ACTION,
    title TEXT NOT NULL,
    size BIGINT NOT NULL,
    path TEXT NOT NULL UNIQUE,
    created_at TIMESTAMP NOT NULL,
    file_type TEXT NOT NULL DEFAULT 'pdf',
    is_saved BOOLEAN NOT NULL DEFAULT FALSE,
    source_url TEXT,
    extracted_text TEXT,
    thumbnail_url TEXT
);

-- REFRESH TOKENS
CREATE TABLE IF NOT EXISTS  refresh_tokens (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE ON UPDATE NO ACTION,
    token TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    unique (user_id)
);

-- CHATS
CREATE TABLE IF NOT EXISTS  chats (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE ON UPDATE NO ACTION,
    title TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- MESSAGES
CREATE TABLE IF NOT EXISTS  messages (
    id UUID PRIMARY KEY,
    chat_id UUID NOT NULL REFERENCES chats(id) ON DELETE CASCADE ON UPDATE NO ACTION,
    is_user BOOLEAN NOT NULL,
    content TEXT NOT NULL, 
    created_at TIMESTAMP NOT NULL,
    context_snapshot JSONB,
    attachments JSONB
);

-- QUIZZES
CREATE TABLE IF NOT EXISTS quizzes (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    document_id UUID NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    description TEXT,
    duration_minutes INTEGER,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP
);

-- QUIZ QUESTIONS
CREATE TABLE IF NOT EXISTS quiz_questions (
    id UUID PRIMARY KEY,
    quiz_id UUID NOT NULL REFERENCES quizzes(id) ON DELETE CASCADE,
    question_no INTEGER NOT NULL,
    question_text TEXT NOT NULL,
    explanation TEXT,  -- Doğru cevap açıklaması
    points INTEGER DEFAULT 1,
    UNIQUE(quiz_id, question_no)
);

-- QUESTION OPTIONS
CREATE TABLE IF NOT EXISTS quiz_question_options (
    id UUID PRIMARY KEY,
    question_id UUID NOT NULL REFERENCES quiz_questions(id) ON DELETE CASCADE,
    option_label CHAR(1) NOT NULL,
    option_text TEXT NOT NULL,
    is_correct BOOLEAN NOT NULL DEFAULT FALSE,
    display_order INTEGER NOT NULL,
    UNIQUE(question_id, option_label),
    CHECK (option_label IN ('A', 'B', 'C', 'D', 'E'))
);

-- QUIZ ATTEMPTS
CREATE TABLE IF NOT EXISTS quiz_attempts (
    id UUID PRIMARY KEY,
    quiz_id UUID NOT NULL REFERENCES quizzes(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id),
    score INTEGER,
    total_questions INTEGER,
    created_at TIMESTAMP DEFAULT NOW(),
    completed_at TIMESTAMP DEFAULT NOW()
);


CREATE TABLE IF NOT EXISTS quiz_attempt_answers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    attempt_id UUID NOT NULL REFERENCES quiz_attempts(id) ON DELETE CASCADE,
    question_no INT NOT NULL,
    selected_option VARCHAR(1) NOT NULL CHECK (selected_option IN ('A', 'B', 'C', 'D', 'E')),
    correct_option VARCHAR(1) NOT NULL CHECK (correct_option IN ('A', 'B', 'C', 'D', 'E')),
    is_correct BOOLEAN NOT NULL,
    created_at TIMESTAMP DEFAULT NOW(),
    UNIQUE(attempt_id, question_no)
);


-- USER SUBSCRIPTIONS
CREATE TABLE IF NOT EXISTS user_subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan_type TEXT NOT NULL DEFAULT 'free',
    revenuecat_id TEXT,
    product_id TEXT,
    entitlement_id TEXT,
    expires_at TIMESTAMPTZ,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    will_renew BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id)
);

-- USAGE TRACKING
CREATE TABLE IF NOT EXISTS usage_tracking (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    action_type TEXT NOT NULL,
    usage_date DATE NOT NULL DEFAULT CURRENT_DATE,
    usage_count INT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, action_type, usage_date)
);

    -- INDEXES
    CREATE INDEX IF NOT EXISTS  idx_quiz_questions_quiz_id ON quiz_questions(quiz_id);
    CREATE INDEX IF NOT EXISTS  idx_quiz_attempts_quiz_user ON quiz_attempts(quiz_id, user_id);
    CREATE INDEX IF NOT EXISTS idx_quiz_attempt_answers_attempt_id ON quiz_attempt_answers(attempt_id);
    CREATE INDEX IF NOT EXISTS idx_usage_tracking_user_date ON usage_tracking(user_id, usage_date);
    CREATE INDEX IF NOT EXISTS idx_user_subscriptions_user_id ON user_subscriptions(user_id);
	`
	_, err := db.Exec(createTableQuery)
	if err != nil {
		panic("Failed to create tables: " + err.Error())
	}

	migrationQuery := `
	-- Backfill: free subscription for existing users without one
	INSERT INTO user_subscriptions (user_id, plan_type, is_active, will_renew)
	SELECT id, 'free', TRUE, TRUE FROM users u
	WHERE NOT EXISTS (SELECT 1 FROM user_subscriptions us WHERE us.user_id = u.id)
	ON CONFLICT (user_id) DO NOTHING;

	-- Backfill: usage_tracking rows for existing users
	INSERT INTO usage_tracking (user_id, action_type, usage_date, usage_count, updated_at)
	SELECT u.id, a.action_type, CURRENT_DATE, 0, NOW()
	FROM users u
	CROSS JOIN (VALUES ('message'), ('ocr'), ('pdf_chat'), ('file_upload'), ('quiz')) AS a(action_type)
	WHERE NOT EXISTS (
		SELECT 1 FROM usage_tracking ut
		WHERE ut.user_id = u.id AND ut.action_type = a.action_type AND ut.usage_date = CURRENT_DATE
	)
	ON CONFLICT (user_id, action_type, usage_date) DO NOTHING;

	-- Add file_type and is_saved columns if they don't exist
	ALTER TABLE files ADD COLUMN IF NOT EXISTS file_type TEXT NOT NULL DEFAULT 'pdf';
	ALTER TABLE files ADD COLUMN IF NOT EXISTS is_saved BOOLEAN NOT NULL DEFAULT FALSE;
	ALTER TABLE files ADD COLUMN IF NOT EXISTS source_url TEXT;
	ALTER TABLE files ADD COLUMN IF NOT EXISTS extracted_text TEXT;
	ALTER TABLE files ADD COLUMN IF NOT EXISTS thumbnail_url TEXT;

	-- Backfill: classify existing files by extension
	UPDATE files SET file_type = 'image'
	WHERE file_type = 'pdf'
	  AND (LOWER(title) LIKE '%.jpg' OR LOWER(title) LIKE '%.jpeg'
	    OR LOWER(title) LIKE '%.png' OR LOWER(title) LIKE '%.webp');
	`
	_, err = db.Exec(migrationQuery)
	if err != nil {
		panic("Failed to run migrations: " + err.Error())
	}

	// Create index for cleaning up old unsaved image files after columns exist
	_, err = db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_files_image_cleanup
		ON files(file_type, is_saved, created_at)
		WHERE file_type = 'image' AND is_saved = FALSE;
	`)
	if err != nil {
		panic("Failed to create index idx_files_image_cleanup: " + err.Error())
	}
}
