-- +goose Up

-- 搜索查询直接对原列使用 ILIKE，因此索引也必须建在原列上；lower() 表达式索引
-- 只有在查询同样改写为 lower(column) 时才能稳定匹配，会违反本批不改查询的边界。
-- +goose StatementBegin
DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS pg_trgm;
EXCEPTION
    WHEN insufficient_privilege THEN
        RAISE EXCEPTION
            USING ERRCODE = '42501',
                  MESSAGE = '无法启用 pg_trgm：迁移账号缺少 CREATE EXTENSION 权限',
                  HINT = '请由数据库管理员预先在当前数据库安装 pg_trgm，或授予迁移账号所需权限后重试';
END $$;
-- +goose StatementEnd

CREATE INDEX idx_posts_title_trgm
    ON posts USING gin (title gin_trgm_ops);
CREATE INDEX idx_posts_content_trgm
    ON posts USING gin (content gin_trgm_ops);
CREATE INDEX idx_users_name_trgm
    ON users USING gin (name gin_trgm_ops);


-- +goose Down

DROP INDEX IF EXISTS idx_users_name_trgm;
DROP INDEX IF EXISTS idx_posts_content_trgm;
DROP INDEX IF EXISTS idx_posts_title_trgm;

-- pg_trgm 可能在本迁移之前已经存在，也可能被同库其它对象共享；回滚只移除本迁移
-- 创建的索引，不擅自删除数据库级扩展。
