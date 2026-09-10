-- +goose Up

-- 审核记录自身不可变；候选值独立于 users.name，版本绑定提交时最近一次已生效改名。
-- 历史记录无法恢复真实候选值，保留 NULL，禁止用当前用户名冒充原始输入。
ALTER TABLE moderation_records
    ADD COLUMN username_candidate varchar(24),
    ADD COLUMN username_revision bigint,
    ADD CONSTRAINT mr_username_snapshot_check CHECK (
        (username_candidate IS NULL AND username_revision IS NULL)
        OR (username_candidate IS NOT NULL AND username_revision IS NOT NULL
            AND user_id IS NOT NULL AND field IS NOT NULL AND field = 'name'
            AND username_revision >= 0
            AND username_candidate = btrim(username_candidate)
            AND username_candidate = normalize(username_candidate, NFKC)
            AND char_length(username_candidate) BETWEEN 2 AND 24
            AND danshi_valid_username_characters(username_candidate))
    );

-- +goose StatementBegin
CREATE FUNCTION danshi_check_username_review_snapshot()
RETURNS trigger LANGUAGE plpgsql AS $func$
DECLARE original moderation_records%ROWTYPE;
BEGIN
    IF NEW.supersedes_id IS NOT NULL THEN
        SELECT * INTO original FROM moderation_records WHERE id = NEW.supersedes_id;
        IF (NEW.username_candidate, NEW.username_revision) IS DISTINCT FROM
           (original.username_candidate, original.username_revision) THEN
            RAISE EXCEPTION '人工复核必须继承原始用户名候选及版本'
                USING ERRCODE = 'check_violation', CONSTRAINT = 'mr_username_snapshot_match';
        END IF;
    END IF;
    RETURN NEW;
END;
$func$;
-- +goose StatementEnd
CREATE TRIGGER trg_username_review_snapshot
    BEFORE INSERT ON moderation_records
    FOR EACH ROW EXECUTE FUNCTION danshi_check_username_review_snapshot();

COMMENT ON COLUMN moderation_records.username_candidate IS '被审用户名的不可变快照；历史缺失值不可由现名回填。';
COMMENT ON COLUMN moderation_records.username_revision IS '提交时最近一次已生效改名的审计 ID，初始为 0；用于阻止旧申请覆盖后续改名。';

-- +goose Down
DROP TRIGGER trg_username_review_snapshot ON moderation_records;
DROP FUNCTION danshi_check_username_review_snapshot();
ALTER TABLE moderation_records DROP CONSTRAINT mr_username_snapshot_check,
    DROP COLUMN username_candidate, DROP COLUMN username_revision;
