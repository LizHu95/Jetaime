-- Preserve stored usernames while making uniqueness case-sensitive.
DROP INDEX user_credentials_username_unique;
CREATE UNIQUE INDEX user_credentials_username_unique ON user_credentials (username);
