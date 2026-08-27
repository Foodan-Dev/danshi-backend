-- +goose Up

DROP INDEX uq_image_assets_public_url;

CREATE UNIQUE INDEX uq_image_assets_public_url
    ON image_assets (public_url) WHERE public_url <> '';


-- +goose Down

-- 多条未完成上传共享空 public_url，旧的全表唯一索引无法容纳这些合法 pending 行。
-- 降级前必须先完成或清理到至多一条，否则显式拒绝回滚。
-- +goose StatementBegin
DO $$
BEGIN
  IF (SELECT count(*) FROM image_assets WHERE public_url = '') > 1 THEN
    RAISE EXCEPTION
      'cannot restore uq_image_assets_public_url: multiple image_assets rows have empty public_url'
      USING ERRCODE = 'unique_violation',
            HINT = 'Complete or expire pending uploads before rolling back migration 00014.';
  END IF;
END
$$;
-- +goose StatementEnd

DROP INDEX uq_image_assets_public_url;

CREATE UNIQUE INDEX uq_image_assets_public_url ON image_assets (public_url);
