-- +goose Up

-- 头像与帖子一致：只有真实引用建立后，资产才从 pending/retired 激活为 ready。
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION danshi_activate_avatar_asset()
RETURNS trigger
LANGUAGE plpgsql
AS $func$
BEGIN
  IF OLD.avatar_image_asset_id IS DISTINCT FROM NEW.avatar_image_asset_id
     AND NEW.avatar_image_asset_id IS NOT NULL THEN
    UPDATE image_assets
       SET status = 'ready', updated_at = now()
     WHERE id = NEW.avatar_image_asset_id
       AND status <> 'ready';
  END IF;
  RETURN NEW;
END;
$func$;
-- +goose StatementEnd

CREATE TRIGGER trg_users_activate_avatar_asset
    AFTER UPDATE OF avatar_image_asset_id ON users
    FOR EACH ROW EXECUTE FUNCTION danshi_activate_avatar_asset();

-- 修正旧版本在 complete 时提前置 ready 留下的无引用资产。引用存在时保持 ready；
-- 回收入口随后会按 created_at 分批处理这些 pending 行。
UPDATE image_assets AS a
   SET status = 'pending', updated_at = now()
 WHERE a.status = 'ready'
   AND NOT EXISTS (SELECT 1 FROM post_images pi WHERE pi.image_asset_id = a.id)
   AND NOT EXISTS (SELECT 1 FROM users u WHERE u.avatar_image_asset_id = a.id);

COMMENT ON COLUMN image_assets.moderation IS
  '机器审核结论，与 status 正交。帖子图片允许在 pending/review 时先建立引用，但帖子保持待审；'
  '头像只有 pass 才允许绑定。block 图片不可新建业务引用。';

-- +goose Down

DROP TRIGGER IF EXISTS trg_users_activate_avatar_asset ON users;
DROP FUNCTION IF EXISTS danshi_activate_avatar_asset();

COMMENT ON COLUMN image_assets.moderation IS
  '机器审核结论，与 status 正交（status 说的是「有没有被引用」，本列说的是「内容合不合规」）。'
  'pending 待审；pass 通过；review 需人工复核；block 判定违规。'
  '审核是异步的，用户点发布时图片可能还没审完，因此**允许引用尚在 pending/review 的图片**，'
  '只有 block 不可引用；真正的约束是「引用了未通过图片的帖子不得进入已发布状态」，'
  '这是跨表规则，由服务层单事务校验（见 go-rewrite-plan §5.2.9）。审核明细见 moderation_records。';
