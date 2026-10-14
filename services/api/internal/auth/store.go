package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) UsernameExists(ctx context.Context, username string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM user_credentials WHERE username=$1
 )`, username).Scan(&exists)
	return exists, err
}

func (s *PostgresStore) CreateUser(ctx context.Context, username, nickname, hash string) (User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	user := User{ID: uuid.NewString(), Username: username, Nickname: nickname}
	if _, err := tx.Exec(ctx, `INSERT INTO users(id,nickname) VALUES ($1,$2)`, user.ID, nickname); err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_credentials(user_id,username,password_hash) VALUES ($1,$2,$3)`, user.ID, username, hash); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "user_credentials_username_unique" {
			return User{}, ErrUsernameTaken
		}
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *PostgresStore) Credentials(ctx context.Context, username string) (User, string, error) {
	var user User
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT u.id::text,c.username,u.nickname,c.password_hash
 FROM user_credentials c JOIN users u ON u.id=c.user_id WHERE c.username=$1`, username).Scan(&user.ID, &user.Username, &user.Nickname, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", errNotFound
	}
	return user, hash, err
}

func (s *PostgresStore) CreateSession(ctx context.Context, userID string, hash []byte, ttl time.Duration) (time.Time, error) {
	var expires time.Time
	err := s.pool.QueryRow(ctx, `INSERT INTO auth_sessions(id,user_id,token_hash,expires_at)
 VALUES ($1,$2,$3,now()+$4 * interval '1 second') RETURNING expires_at`, uuid.NewString(), userID, hash, int64(ttl/time.Second)).Scan(&expires)
	return expires, err
}

func (s *PostgresStore) RevokeSession(ctx context.Context, hash []byte) error {
	// Repeating logout is harmless, but unknown and expired tokens are rejected.
	result, err := s.pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at=COALESCE(revoked_at,now())
 WHERE token_hash=$1 AND expires_at>now()`, hash)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrUnauthorized
	}
	return nil
}
