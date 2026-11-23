package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	protectedRoomV2Prefix = "pr2_"
	meetingIDDigits       = 10
	meetingPasswordDigits = 6
)

var protectedRoomDBState struct {
	sync.Mutex
	dsn string
	db  *sql.DB
}

func protectedRoomDB() (*sql.DB, error) {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return nil, errors.New("database is not configured")
	}

	protectedRoomDBState.Lock()
	defer protectedRoomDBState.Unlock()
	if protectedRoomDBState.db != nil && protectedRoomDBState.dsn == dsn {
		return protectedRoomDBState.db, nil
	}
	if protectedRoomDBState.db != nil {
		_ = protectedRoomDBState.db.Close()
		protectedRoomDBState.db = nil
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	protectedRoomDBState.dsn = dsn
	protectedRoomDBState.db = db
	return db, nil
}

func createStoredProtectedRoom(ctx context.Context) (roomID, meetingID, password string, err error) {
	db, err := protectedRoomDB()
	if err != nil {
		return "", "", "", err
	}

	for attempt := 0; attempt < 10; attempt++ {
		roomID, err = randomStoredProtectedRoomID()
		if err != nil {
			return "", "", "", err
		}
		meetingID, err = randomNumericCode(meetingIDDigits)
		if err != nil {
			return "", "", "", err
		}
		password, err = randomNumericCode(meetingPasswordDigits)
		if err != nil {
			return "", "", "", err
		}

		result, insertErr := db.ExecContext(ctx, `
			INSERT INTO protected_rooms (meeting_id, room_id, password_hash)
			VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`,
			meetingID, roomID, protectedRoomPasswordHash(roomID, password))
		if insertErr != nil {
			return "", "", "", insertErr
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return "", "", "", err
		}
		if inserted == 1 {
			return roomID, meetingID, password, nil
		}
	}
	return "", "", "", errors.New("could not allocate a unique meeting ID")
}

func resolveStoredMeeting(ctx context.Context, meetingID, password string) (string, bool, error) {
	if !validNumericCode(meetingID, meetingIDDigits) ||
		!validNumericCode(password, meetingPasswordDigits) {
		return "", false, nil
	}
	db, err := protectedRoomDB()
	if err != nil {
		return "", false, err
	}

	var roomID string
	var storedHash []byte
	err = db.QueryRowContext(ctx,
		"SELECT room_id, password_hash FROM protected_rooms WHERE meeting_id = $1",
		meetingID,
	).Scan(&roomID, &storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	expectedHash := protectedRoomPasswordHash(roomID, password)
	return roomID, len(storedHash) == len(expectedHash) &&
		subtle.ConstantTimeCompare(storedHash, expectedHash) == 1, nil
}

func protectedRoomPasswordHash(roomID, password string) []byte {
	mac := hmac.New(sha256.New, roomAccessSecret())
	_, _ = mac.Write([]byte("protected-room-password-v2\x00" + roomID + "\x00" + password))
	return mac.Sum(nil)
}

func randomStoredProtectedRoomID() (string, error) {
	nonce := make([]byte, protectedNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	nonceText := strings.ToLower(protectedBase32.EncodeToString(nonce))
	mac := hmac.New(sha256.New, roomAccessSecret())
	_, _ = mac.Write([]byte("protected-room-id-v2\x00" + nonceText))
	tag := strings.ToLower(protectedBase32.EncodeToString(mac.Sum(nil)[:10]))
	return protectedRoomV2Prefix + nonceText + "_" + tag, nil
}

func isProtectedRoomV2ID(roomID string) bool {
	if !strings.HasPrefix(roomID, protectedRoomV2Prefix) {
		return false
	}
	remainder := strings.TrimPrefix(roomID, protectedRoomV2Prefix)
	parts := strings.Split(remainder, "_")
	if len(parts) != 2 || len(parts[0]) != 16 || len(parts[1]) != 16 {
		return false
	}
	for _, char := range parts[0] + parts[1] {
		if !((char >= 'a' && char <= 'z') || (char >= '2' && char <= '7')) {
			return false
		}
	}
	return true
}

func validProtectedRoomV2ID(roomID string) bool {
	if !isProtectedRoomV2ID(roomID) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(roomID, protectedRoomV2Prefix), "_")
	mac := hmac.New(sha256.New, roomAccessSecret())
	_, _ = mac.Write([]byte("protected-room-id-v2\x00" + parts[0]))
	expected := strings.ToLower(protectedBase32.EncodeToString(mac.Sum(nil)[:10]))
	return subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expected)) == 1
}

func validNumericCode(value string, digits int) bool {
	if len(value) != digits {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func randomNumericCode(digits int) (string, error) {
	if digits < 1 || digits > 18 {
		return "", fmt.Errorf("unsupported numeric code length %d", digits)
	}
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	value, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", digits, value.Int64()), nil
}
