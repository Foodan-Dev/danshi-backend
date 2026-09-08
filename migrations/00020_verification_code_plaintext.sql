-- 验证码同步发送；挑战行保存明文，摘要继续用于校验。
-- +goose Up
ALTER TABLE email_verification_codes
    ADD COLUMN code varchar(6),
    ADD CONSTRAINT email_verification_codes_code_check
        CHECK (code IS NULL OR code ~ '^[0-9]{6}$');
COMMENT ON COLUMN email_verification_codes.code IS
    '六位验证码明文；消费、替换或错误次数耗尽时清理，不得输出到日志或 API。过期由 expires_at 判定，旧明文在下次状态写入时清理。';
COMMENT ON COLUMN email_verification_codes.code_digest IS
    '验证码 HMAC 摘要，用于校验；code 列另存原始验证码。';
-- +goose Down
ALTER TABLE email_verification_codes DROP CONSTRAINT email_verification_codes_code_check;
ALTER TABLE email_verification_codes DROP COLUMN code;
COMMENT ON COLUMN email_verification_codes.code_digest IS '验证码摘要，明文不落库。';
