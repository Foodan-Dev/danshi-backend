-- 全量正式迁移的 schema 回归测试。
-- 用法：对一个刚跑完全部迁移的空库执行；任一断言失败即以非 0 退出。
--   psql -X -v ON_ERROR_STOP=1 -f migrations/testdata/schema_smoke.sql
--
-- ⚠ CI 必须跑**两条独立链路**，不要串成一条：
--   A 数据链路：干净库 → up → 本脚本
--   B 结构链路：干净库 → up → down → up
-- 不能在跑完本脚本的库上执行 down：本脚本会留下引用字典表的提议行，
-- 而 dictionary_suggestions.parent_canteen_id 是 RESTRICT，
-- 种子回滚会正确地失败。那是约束在正常工作，不是 down 脚本有问题。
--
-- ⚠ 所有期望值都必须走 _assert()，靠 RAISE EXCEPTION 让 psql 以非 0 退出。
--   历史教训：早期版本大量用 `SELECT ...;  \echo '期望 X'`，那只是把结果打印出来，
--   值算错了脚本照样退出 0，CI 门禁形同虚设。禁止再引入这种「假断言」。

\set ON_ERROR_STOP on
\pset border 2

-- +----------------------------------------------------------+
-- | 断言辅助                                                   |
-- +----------------------------------------------------------+
CREATE OR REPLACE FUNCTION _assert(cond boolean, label text)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  IF cond IS NOT TRUE THEN
    RAISE EXCEPTION 'FAIL: %', label;
  END IF;
  RAISE NOTICE 'PASS  %', label;
END $$;

-- 断言某段 SQL 抛出预期错误类型
CREATE OR REPLACE FUNCTION _assert_rejects(stmt text, expected_sqlstates text[], label text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE got text;
BEGIN
  BEGIN
    EXECUTE stmt;
  EXCEPTION WHEN others THEN
    got := SQLSTATE;
    IF got = ANY (expected_sqlstates) THEN
      RAISE NOTICE 'PASS  %', label;
      RETURN;
    END IF;
    RAISE EXCEPTION 'FAIL: % —— 期望 SQLSTATE %，实际 % (%)', label, expected_sqlstates, got, SQLERRM;
  END;
  RAISE EXCEPTION 'FAIL: % —— 语句本应被拒绝，却执行成功', label;
END $$;

-- 常用 SQLSTATE：23514 check_violation / 23505 unique_violation
-- 23503 foreign_key_violation / 23001 restrict_violation / 22001 string_data_right_truncation

\echo '########## 0. 字典表种子 ##########'
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM canteens)        = 15, '种子 canteens = 15');
  PERFORM _assert((SELECT count(*) FROM canteen_windows) = 0,  '种子 canteen_windows = 0（由管理端录入）');
  PERFORM _assert((SELECT count(*) FROM cuisines)        = 6,  '种子 cuisines = 6');
  PERFORM _assert((SELECT count(*) FROM flavors)         = 16, '种子 flavors = 16');
  PERFORM _assert((SELECT count(DISTINCT campus) FROM canteens) = 4, '种子覆盖 4 个校区');
END $$;

\echo ''
\echo '########## 装载基础数据 ##########'
INSERT INTO users (id,email,password_hash,name,gender) VALUES
 (1,'Alice@m.fudan.edu.cn','$2b$12$x','alice','female'),
 (2,'bob@m.fudan.edu.cn','$2b$12$x','bob','male'),
 (3,'admin@fdueat.com','$2b$12$x','admin',NULL);
INSERT INTO user_roles (user_id,role,granted_by) VALUES (3,'moderator',3);
INSERT INTO user_role_records (user_id,role,action,actor_id) VALUES (3,'moderator','grant',3);
INSERT INTO image_assets (id,uploader_id,purpose,object_key,public_url,content_type,status) VALUES
 (101,1,'post','posts/a/1.jpg','https://img/1.jpg','image/jpeg','ready'),
 (102,1,'avatar','avatars/a/1.jpg','https://img/av.jpg','image/jpeg','ready'),
 (103,1,'avatar','avatars/a/2.jpg','https://img/av2.jpg','image/jpeg','pending');
UPDATE users SET avatar_image_asset_id=102 WHERE id=1;
INSERT INTO canteen_windows (id,canteen_id,name) VALUES
 (201,(SELECT id FROM canteens WHERE code='canteen-nanqu'),'一楼麻辣烫'),
 (202,(SELECT id FROM canteens WHERE code='canteen-jiangwan'),'二楼盖浇饭');
BEGIN;
INSERT INTO posts (id,author_id,post_type,share_type,status,category,title,content,canteen_id,canteen_window_id,cuisine_id,price,current_revision)
 VALUES (1001,1,'share','recommend','approved','food','测试','正文',
         (SELECT id FROM canteens WHERE code='canteen-nanqu'), 201,
         (SELECT id FROM cuisines WHERE name='中式'), 18.50,2);
INSERT INTO post_images (post_id,position,image_asset_id) VALUES (1001,0,101);
INSERT INTO tags (id,name) VALUES (301,'便宜'),(302,'Spicy');
INSERT INTO post_tags (post_id,tag_id) VALUES (1001,301),(1001,302);
INSERT INTO post_flavors (post_id,flavor_id,stance,post_type)
 SELECT 1001, id, 'has', 'share' FROM flavors WHERE name IN ('麻辣','香辣');
INSERT INTO post_histories (id,post_id,revision,edited_by,snapshot,edit_reason)
 VALUES
 (1,1001,1,1,'{"title":"测试（编辑前）","content":"旧正文","tags":["便宜"]}'::jsonb,'修正文案'),
 (2,1001,2,1,'{"title":"测试","content":"正文","tags":["便宜","Spicy"]}'::jsonb,NULL);
COMMIT;
\echo 'OK'

\echo ''
\echo '########## 1. users / image_assets / posts 枚举与范围约束 ##########'
DO $$ BEGIN
  PERFORM _assert_rejects($q$INSERT INTO user_roles (user_id,role) VALUES (1,'admin')$q$,
    ARRAY['23514'], 'user_roles.role 枚举收敛且 admin 已消失');
  PERFORM _assert_rejects($q$INSERT INTO users (email,password_hash,name) VALUES ('noatsign','x','bad_email')$q$,
    ARRAY['23514'], 'users.email 必须含 @');
  PERFORM _assert_rejects($q$INSERT INTO users (email,password_hash,name,gender) VALUES ('g@fdueat.com','x','bad_gender','男')$q$,
    ARRAY['23514'], 'users.gender 枚举收敛(male/female/other)');
  PERFORM _assert_rejects($q$INSERT INTO users (email,password_hash,name) VALUES ('ALICE@m.fudan.edu.cn','x','alice_email')$q$,
    ARRAY['23505'], 'users.email 大小写不敏感唯一');
  PERFORM _assert_rejects($q$INSERT INTO users (email,password_hash,name) VALUES ('empty-name@fdueat.com','x','')$q$,
    ARRAY['23514'], 'users.name 不接受空串');
  PERFORM _assert_rejects($q$INSERT INTO users (email,password_hash,name) VALUES ('name-case@fdueat.com','x','ALICE')$q$,
    ARRAY['23505'], 'users.name 大小写不敏感唯一');
  PERFORM _assert_rejects($q$INSERT INTO users (email,password_hash,name) VALUES ('name-width@fdueat.com','x','Ａlice')$q$,
    ARRAY['23514'], 'users.name 必须先做 NFKC 归一化');
  PERFORM _assert_rejects($q$INSERT INTO users (email,password_hash,name) VALUES ('name-symbol@fdueat.com','x','alice-name')$q$,
    ARRAY['23514'], 'users.name 只接受文字、数字和下划线');
  PERFORM _assert((SELECT count(*) FROM user_name_claims WHERE user_id IN (1,2,3))=3,
                  '直接创建用户同时追加 name 占用记录');

  PERFORM _assert_rejects($q$INSERT INTO image_assets (purpose,object_key,public_url,content_type) VALUES ('post','k1','u1','image/jpeg') $q$ ||
    $q$; INSERT INTO image_assets (purpose,object_key,public_url,content_type,status) VALUES ('post','k2','u2','image/jpeg','gone')$q$,
    ARRAY['23514'], 'image_assets.status 枚举收敛');
  PERFORM _assert_rejects($q$INSERT INTO image_assets (purpose,object_key,public_url,content_type) VALUES ('banner','k3','u3','image/jpeg')$q$,
    ARRAY['23514'], 'image_assets.purpose 枚举收敛');
  PERFORM _assert_rejects($q$INSERT INTO image_assets (purpose,object_key,public_url,content_type,size) VALUES ('post','k4','u4','image/jpeg',0)$q$,
    ARRAY['23514'], 'image_assets.size 必须为正');
  PERFORM _assert_rejects($q$INSERT INTO image_assets (purpose,object_key,public_url,content_type) VALUES ('post','posts/a/1.jpg','u5','image/jpeg')$q$,
    ARRAY['23505'], 'image_assets.object_key 唯一');
  PERFORM _assert_rejects($q$INSERT INTO image_assets (purpose,object_key,public_url,content_type) VALUES ('post','k6','https://img/1.jpg','image/jpeg')$q$,
    ARRAY['23505'], 'image_assets.public_url 唯一');
  PERFORM _assert_rejects($q$DELETE FROM image_assets WHERE id=101$q$,
    ARRAY['23001'], 'image_assets 禁止物理删除');

  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,category,title,content) VALUES (1,'companion','food','x','y')$q$,
    ARRAY['23514'], 'posts.post_type 拒绝 companion');
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,share_type,category,title,content) VALUES (1,'share','maybe','food','x','y')$q$,
    ARRAY['23514'], 'posts.share_type 枚举收敛');
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,share_type,status,category,title,content) VALUES (1,'share','recommend','archived','food','x','y')$q$,
    ARRAY['23514'], 'posts.status 枚举收敛');
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,share_type,category,title,content) VALUES (1,'share','recommend','drink','x','y')$q$,
    ARRAY['23514'], 'posts.category 枚举收敛');
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,share_type,status,category,title,content) VALUES (1,'seeking','recommend','approved','food','x','y')$q$,
    ARRAY['23514'], 'seeking 不得带 share_type');
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,status,category,title,content) VALUES (1,'share','approved','food','x','y')$q$,
    ARRAY['23514'], 'share 非草稿必须有 share_type');
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,share_type,category,title,content,price) VALUES (1,'share','recommend','food','x','y',-1)$q$,
    ARRAY['23514'], 'posts.price 不得为负');
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,share_type,category,title,content,view_count) VALUES (1,'share','recommend','food','x','y',-1)$q$,
    ARRAY['23001','23514'], 'posts 计数器不得为负（写保护先于 CHECK 生效）');
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,share_type,category,title,content,budget_min) VALUES (1,'share','recommend','food','x','y',10)$q$,
    ARRAY['23514'], 'budget_min/max 必须成对');
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,share_type,category,title,content,budget_min,budget_max) VALUES (1,'share','recommend','food','x','y',100,10)$q$,
    ARRAY['23514'], 'budget_max >= budget_min');
  PERFORM _assert_rejects($q$INSERT INTO post_images (post_id,position,image_asset_id) VALUES (1001,9,101)$q$,
    ARRAY['23514'], 'post_images.position < 9');
  PERFORM _assert_rejects($q$INSERT INTO follows (follower_id,following_id) VALUES (1,1)$q$,
    ARRAY['23514'], '禁止自关注');
END $$;

UPDATE users SET name='bob2' WHERE id=2;
DO $$ BEGIN
  PERFORM _assert_rejects($q$INSERT INTO users (email,password_hash,name) VALUES ('old-name@fdueat.com','x','bob')$q$,
    ARRAY['23505'], '历史 name 不得被另一账号接管');
  PERFORM _assert((SELECT count(*) FROM user_name_claims WHERE user_id=2)=2,
                  'name 修改追加占用记录而不覆盖旧值');
  PERFORM _assert((SELECT count(*) FROM user_name_change_records WHERE user_id=2)=1,
                  'name 修改追加不可篡改审计记录');
  PERFORM _assert((SELECT old_name || '>' || new_name FROM user_name_change_records WHERE user_id=2)='bob>bob2',
                  'name 审计记录保存修改前后值');
  PERFORM _assert_rejects($q$UPDATE user_name_change_records SET new_name='rewrite' WHERE user_id=2$q$,
    ARRAY['23001'], 'name 审计记录不可修改');
  PERFORM _assert_rejects($q$DELETE FROM user_name_change_records WHERE user_id=2$q$,
    ARRAY['23001'], 'name 审计记录不可删除');
  PERFORM _assert_rejects($q$UPDATE user_name_claims SET name='rewrite' WHERE user_id=2$q$,
    ARRAY['23001'], 'name 占用记录不可修改');
  PERFORM _assert_rejects($q$DELETE FROM user_name_claims WHERE user_id=2$q$,
    ARRAY['23001'], 'name 占用记录不可删除');
END $$;

\echo ''
\echo '########## 1a-1. RBAC 关联与角色流水 ##########'
DELETE FROM user_roles WHERE user_id=3 AND role='moderator';
INSERT INTO user_role_records (user_id,role,action,actor_id) VALUES (3,'moderator','revoke',3);
INSERT INTO user_roles (user_id,role,granted_by) VALUES (3,'moderator',3);
INSERT INTO user_role_records (user_id,role,action,actor_id) VALUES (3,'moderator','grant',3);
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM user_roles WHERE user_id=3 AND role='moderator')=1,
                  '角色撤销后可再次授予');
  PERFORM _assert((SELECT count(*) FROM user_role_records WHERE user_id=3 AND role='moderator')=3,
                  'grant/revoke/grant 三次动作完整留痕');
  PERFORM _assert_rejects($q$UPDATE user_role_records SET action='revoke' WHERE id=(SELECT min(id) FROM user_role_records)$q$,
    ARRAY['23001'], 'user_role_records 禁止 UPDATE');
  PERFORM _assert_rejects($q$DELETE FROM user_role_records WHERE id=(SELECT min(id) FROM user_role_records)$q$,
    ARRAY['23001'], 'user_role_records 禁止 DELETE');
END $$;

-- 草稿豁免：正例
BEGIN;
INSERT INTO posts (id,author_id,post_type,status,category,title,content) VALUES (1099,1,'share','draft','food','草稿','y');
INSERT INTO post_histories (id,post_id,revision,edited_by,snapshot) VALUES (1099,1099,1,1,'{}');
COMMIT;
DO $$ BEGIN PERFORM _assert((SELECT count(*) FROM posts WHERE id=1099)=1, 'share 草稿可无 share_type'); END $$;
-- 清理用例数据：日常路径是软删除，物理清除必须显式开运维逃生阀
BEGIN; SET LOCAL danshi.allow_hard_delete='on'; DELETE FROM posts WHERE id=1099; COMMIT;

\echo ''
\echo '########## 1b. 标签与词表约束 ##########'
DO $$ BEGIN
  PERFORM _assert_rejects($q$INSERT INTO tags (name) VALUES ('spicy')$q$,        ARRAY['23505'], 'tags 大小写不敏感唯一');
  PERFORM _assert_rejects($q$INSERT INTO tags (name) VALUES ('   ')$q$,          ARRAY['23514'], 'tags 不接受空白名');
  PERFORM _assert_rejects($q$INSERT INTO tags (name) VALUES (' 有空格 ')$q$,     ARRAY['23514'], 'tags 必须已 trim');
  PERFORM _assert_rejects($q$INSERT INTO tags (name) VALUES ('这个标签一共有十一个字')$q$, ARRAY['22001'], 'tags 限 10 字符（11 字被拒）');
  PERFORM _assert_rejects($q$DELETE FROM tags WHERE id=301$q$,                   ARRAY['23001','23503'], '被引用的 tag 不可删');
  PERFORM _assert_rejects($q$INSERT INTO post_flavors (post_id,flavor_id,stance,post_type) VALUES (1001,(SELECT id FROM flavors WHERE name='清淡'),'like','share')$q$,
    ARRAY['23514'], 'post_flavors.stance 枚举收敛');
  PERFORM _assert_rejects($q$INSERT INTO post_flavors (post_id,flavor_id,stance,post_type) VALUES (1001,(SELECT id FROM flavors WHERE name='麻辣'),'avoid','seeking')$q$,
    ARRAY['23505'], '同一口味不能既 has 又 avoid');
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,share_type,category,title,content,canteen_id) VALUES (1,'share','recommend','food','x','y',9999)$q$,
    ARRAY['23503'], 'canteen_id 必须指向字典表');
END $$;
-- 正例：刚好 10 字符
INSERT INTO tags (id,name) VALUES (303,'刚好十个字的标签啦嗯');
DO $$ BEGIN PERFORM _assert((SELECT char_length(name) FROM tags WHERE id=303)=10, '10 字标签可入库'); END $$;

\echo ''
\echo '########## 1c. 餐厅↔窗口级联一致性 ##########'
DO $$
DECLARE nanqu bigint; jiangwan bigint;
BEGIN
  SELECT id INTO nanqu    FROM canteens WHERE code='canteen-nanqu';
  SELECT id INTO jiangwan FROM canteens WHERE code='canteen-jiangwan';
  PERFORM _assert_rejects(format($q$INSERT INTO posts (author_id,post_type,share_type,category,title,content,canteen_id,canteen_window_id) VALUES (1,'share','recommend','food','串味','y',%s,201)$q$, jiangwan),
    ARRAY['23503'], '窗口必须属于所选餐厅');
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,share_type,category,title,content,canteen_window_id) VALUES (1,'share','recommend','food','孤窗口','y',201)$q$,
    ARRAY['23514'], '填了窗口就必须填餐厅');
  PERFORM _assert_rejects(format($q$INSERT INTO canteen_windows (canteen_id,name) VALUES (%s,'一楼麻辣烫')$q$, nanqu),
    ARRAY['23505'], '同餐厅同楼层内窗口名唯一（楼层均为空也算重复）');
  -- 楼层可空；同名不同层视为两个窗口
  EXECUTE format($q$INSERT INTO canteen_windows (id,canteen_id,name,floor) VALUES (204,%s,'麻辣烫','1F')$q$, nanqu);
  EXECUTE format($q$INSERT INTO canteen_windows (id,canteen_id,name,floor) VALUES (205,%s,'麻辣烫','B1')$q$, nanqu);
  PERFORM _assert((SELECT count(*) FROM canteen_windows WHERE name='麻辣烫')=2, '同名不同楼层可共存');
  PERFORM _assert_rejects(format($q$INSERT INTO canteen_windows (canteen_id,name,floor) VALUES (%s,'麻辣烫','1F')$q$, nanqu),
    ARRAY['23505'], '同餐厅同楼层同名被拒');
  PERFORM _assert_rejects(format($q$INSERT INTO canteen_windows (canteen_id,name,floor) VALUES (%s,'窗口','  ')$q$, nanqu),
    ARRAY['23514'], 'floor 不接受空白串');
  -- 正例
  EXECUTE format($q$INSERT INTO posts (id,author_id,post_type,share_type,category,title,content,canteen_id) VALUES (1002,1,'share','recommend','food','只知餐厅','y',%s)$q$, nanqu);
  INSERT INTO posts (id,author_id,post_type,share_type,category,title,content) VALUES (1003,1,'share','recommend','food','都不知道','y');
  INSERT INTO post_histories (id,post_id,revision,edited_by,snapshot) VALUES
    (1002,1002,1,1,'{}'),(1003,1003,1,1,'{}');
  EXECUTE format($q$INSERT INTO canteen_windows (id,canteen_id,name) VALUES (203,%s,'一楼麻辣烫')$q$, jiangwan);
  PERFORM _assert((SELECT count(*) FROM posts WHERE id IN (1002,1003))=2, '餐厅/窗口均可留空');
  PERFORM _assert((SELECT count(*) FROM canteen_windows WHERE name='一楼麻辣烫')=2, '不同餐厅可有同名窗口');
END $$;
BEGIN; SET LOCAL danshi.allow_hard_delete='on'; DELETE FROM posts WHERE id IN (1002,1003); COMMIT;
DELETE FROM canteen_windows WHERE id IN (203,204,205);

\echo ''
\echo '########## 1d. comments：同帖 + 楼号一致，存真实链、展示拍扁 ##########'
BEGIN;
INSERT INTO posts (id,author_id,post_type,share_type,status,category,title,content) VALUES (1004,2,'share','recommend','approved','food','另一帖','y');
INSERT INTO post_histories (id,post_id,revision,edited_by,snapshot) VALUES (1004,1004,1,2,'{}');
-- 行为 1：点帖子的评论按钮 → 楼主评论
INSERT INTO comments (id,post_id,author_id,parent_id,root_id,reply_to_user_id,content,moderation,current_revision)
 VALUES (2001,1001,2,NULL,NULL,1,'楼主评论','pass',2);
-- 行为 2：点楼主评论的回复 → parent_id = root_id = 楼主评论
INSERT INTO comments (id,post_id,author_id,parent_id,root_id,reply_to_user_id,content,moderation)
 VALUES (2002,1001,1,2001,2001,2,'回复楼主','pass');
-- 行为 3：点楼内非 root 评论的回复 → parent 指向它，root 沿用同一楼
INSERT INTO comments (id,post_id,author_id,parent_id,root_id,reply_to_user_id,content,moderation)
 VALUES (2003,1001,2,2002,2001,1,'回复楼内回复','pass');
INSERT INTO comments (id,post_id,author_id,parent_id,root_id,reply_to_user_id,content,moderation)
 VALUES (2004,1001,1,2003,2001,2,'第四层，存储允许','pass');
INSERT INTO comment_histories (id,comment_id,revision,edited_by,content) VALUES
 (1,2001,1,2,'楼主评论（编辑前）'),
 (2,2001,2,2,'楼主评论'),
 (3,2002,1,1,'回复楼主'),
 (4,2003,1,2,'回复楼内回复'),
 (5,2004,1,1,'第四层，存储允许');
COMMIT;
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM comments WHERE root_id=2001)=3, '同一楼下 3 条回复（任意深度都拍扁到同一楼）');
  PERFORM _assert((SELECT effective_root_id FROM comments WHERE id=2001)=2001, '楼主评论 effective_root = 自身');
  PERFORM _assert((SELECT effective_root_id FROM comments WHERE id=2004)=2001, '深层回复 effective_root = 楼主');

  PERFORM _assert_rejects($q$INSERT INTO comments (post_id,author_id,parent_id,root_id,reply_to_user_id,content) VALUES (1004,1,2001,2001,2,'跨帖回复')$q$,
    ARRAY['23503'], '回复必须与父评论同帖');
  PERFORM _assert_rejects($q$INSERT INTO comments (post_id,author_id,parent_id,root_id,reply_to_user_id,content) VALUES (1001,1,2003,2003,2,'楼号写错')$q$,
    ARRAY['23503'], 'root_id 必须等于父评论所在的楼');
  PERFORM _assert_rejects($q$INSERT INTO comments (post_id,author_id,parent_id,reply_to_user_id,content) VALUES (1001,1,2001,2,'有 parent 无 root')$q$,
    ARRAY['23514'], 'parent_id 与 root_id 必须成对');
  PERFORM _assert_rejects($q$INSERT INTO comments (post_id,author_id,root_id,reply_to_user_id,content) VALUES (1001,1,2001,1,'有 root 无 parent')$q$,
    ARRAY['23514'], 'root_id 与 parent_id 必须成对');
  -- 计数列只读触发器先于 CHECK 生效，两层防线都算通过
  PERFORM _assert_rejects($q$UPDATE comments SET reply_count=1 WHERE id=2002$q$,
    ARRAY['23001','23514'], 'reply_count 不可直接写（且只能出现在楼主评论上）');
  PERFORM _assert_rejects($q$INSERT INTO comments (post_id,author_id,reply_to_user_id,content,like_count) VALUES (1001,1,1,'x',-1)$q$,
    ARRAY['23001','23514'], 'comments 计数器不得为负（写保护先于 CHECK 生效）');
  PERFORM _assert_rejects($q$UPDATE comments SET deleted_at=now() WHERE id=2004$q$,
    ARRAY['23514'], '软删除必须同时写 deleted_reason');
  PERFORM _assert_rejects($q$UPDATE comments SET deleted_at=now(), deleted_reason='whatever' WHERE id=2004$q$,
    ARRAY['23514'], 'deleted_reason 枚举收敛');
  PERFORM _assert_rejects($q$UPDATE comments SET moderation='unknown' WHERE id=2004$q$,
    ARRAY['23514'], '评论当前审核状态枚举收敛');
END $$;

UPDATE comments SET moderation='review' WHERE id=2002;
DO $$ BEGIN
  PERFORM _assert((SELECT reply_count FROM comments WHERE id=2001)=2,
    '待复核回复不计入楼层回复数');
  PERFORM _assert((SELECT comment_count FROM posts WHERE id=1001)=3,
    '待复核回复不计入帖子评论数');
END $$;
UPDATE comments SET moderation='pass' WHERE id=2002;
UPDATE comments SET moderation='review' WHERE id=2001;
DO $$ BEGIN
  PERFORM _assert((SELECT comment_count FROM posts WHERE id=1001)=0,
    '根评论待复核时整层楼不计入帖子评论数');
  PERFORM _assert((SELECT count(*) FROM comments WHERE root_id=2001 AND moderation='pass')=3,
    '隐藏整层楼不得改写回复行审核状态');
  PERFORM _assert((SELECT reply_count FROM comments WHERE id=2001)=3,
    '根楼隐藏时保留有效回复派生数供恢复使用');
END $$;
UPDATE comments SET moderation='pass' WHERE id=2001;

\echo '########## 1d-1. 软删除防线：内容一律不可物理删除 ##########'
DO $$ BEGIN
  PERFORM _assert_rejects($q$DELETE FROM users WHERE id=2$q$,    ARRAY['23001'], 'users 禁止物理删除');
  PERFORM _assert_rejects($q$DELETE FROM posts WHERE id=1001$q$, ARRAY['23001'], 'posts 禁止物理删除');
  PERFORM _assert_rejects($q$DELETE FROM tags WHERE id=302$q$,   ARRAY['23001'], 'tags 禁止物理删除');
  PERFORM _assert_rejects($q$UPDATE posts SET deleted_at=now() WHERE id=1001$q$,
    ARRAY['23514'], '帖子软删除必须写 deleted_reason');
  PERFORM _assert_rejects($q$UPDATE posts SET deleted_at=now(), deleted_reason='whatever' WHERE id=1001$q$,
    ARRAY['23514'], 'posts.deleted_reason 枚举收敛');
  -- 关联表是「动作」不是「内容」，取消点赞必须能真删
  PERFORM _assert_rejects($q$DELETE FROM comments WHERE id=2001$q$, ARRAY['23001'], 'comments 禁止物理删除');
END $$;
-- 运维逃生阀：事务局部开启后可物理清除
INSERT INTO tags (id,name) VALUES (399,'待清理');
BEGIN; SET LOCAL danshi.allow_hard_delete='on'; DELETE FROM tags WHERE id=399; COMMIT;
DO $$ BEGIN PERFORM _assert((SELECT count(*) FROM tags WHERE id=399)=0, '运维逃生阀可物理清除'); END $$;

\echo ''
\echo '########## 1d-1b. 关联表：动作可反复取消与重做 ##########'
-- 硬原则：关联表是「动作」不是「内容」，不做软删除，但必须保证取消后能再次创建，
-- 且唯一性、幂等、级联、计数器四项都正确。
DO $$
DECLARE c1 int; c2 int; c3 int;
BEGIN
  -- 点赞 → 取消 → 再点赞，计数器必须回到 1
  INSERT INTO post_likes (user_id,post_id) VALUES (2,1001);
  SELECT like_count INTO c1 FROM posts WHERE id=1001;
  DELETE FROM post_likes WHERE user_id=2 AND post_id=1001;
  SELECT like_count INTO c2 FROM posts WHERE id=1001;
  INSERT INTO post_likes (user_id,post_id) VALUES (2,1001);
  SELECT like_count INTO c3 FROM posts WHERE id=1001;
  PERFORM _assert((c1,c2,c3) = (1,0,1)::record, '点赞 → 取消 → 再点赞，计数器 1→0→1');
  PERFORM _assert_rejects($q$INSERT INTO post_likes (user_id,post_id) VALUES (2,1001)$q$,
    ARRAY['23505'], '重复点赞被唯一约束拒绝（应用层用 ON CONFLICT 做幂等）');
  DELETE FROM post_likes WHERE user_id=2 AND post_id=1001;

  -- 收藏
  INSERT INTO favorites (user_id,post_id) VALUES (2,1001);
  DELETE FROM favorites WHERE user_id=2 AND post_id=1001;
  INSERT INTO favorites (user_id,post_id) VALUES (2,1001);
  PERFORM _assert((SELECT favorite_count FROM posts WHERE id=1001)=1, '收藏 → 取消 → 再收藏，计数器正确');
  DELETE FROM favorites WHERE user_id=2 AND post_id=1001;

  -- 关注
  INSERT INTO follows (follower_id,following_id) VALUES (2,1);
  DELETE FROM follows WHERE follower_id=2 AND following_id=1;
  INSERT INTO follows (follower_id,following_id) VALUES (2,1);
  PERFORM _assert((SELECT count(*) FROM follows WHERE follower_id=2)=1, '关注 → 取关 → 再关注');
  DELETE FROM follows WHERE follower_id=2 AND following_id=1;

  -- 帖子↔标签
  DELETE FROM post_tags WHERE post_id=1001 AND tag_id=302;
  INSERT INTO post_tags (post_id,tag_id) VALUES (1001,302);
  PERFORM _assert((SELECT count(*) FROM post_tags WHERE post_id=1001 AND tag_id=302)=1, '帖子标签可移除后重新添加');

  -- 帖子↔口味
  DELETE FROM post_flavors WHERE post_id=1001 AND flavor_id=(SELECT id FROM flavors WHERE name='香辣');
  INSERT INTO post_flavors (post_id,flavor_id,stance,post_type) VALUES (1001,(SELECT id FROM flavors WHERE name='香辣'),'has','share');
  PERFORM _assert((SELECT count(*) FROM post_flavors WHERE post_id=1001)=2, '帖子口味可移除后重新添加');

  -- 评论点赞
  INSERT INTO comment_likes (user_id,comment_id) VALUES (2,2001);
  DELETE FROM comment_likes WHERE user_id=2 AND comment_id=2001;
  INSERT INTO comment_likes (user_id,comment_id) VALUES (2,2001);
  PERFORM _assert((SELECT like_count FROM comments WHERE id=2001)=1, '评论点赞取消后可重建，计数正确');
  DELETE FROM comment_likes WHERE user_id=2 AND comment_id=2001;
  -- 重复插入一律被唯一约束拒
  PERFORM _assert_rejects($q$INSERT INTO favorites (user_id,post_id) VALUES (1,1001); INSERT INTO favorites (user_id,post_id) VALUES (1,1001)$q$,
    ARRAY['23505'], '重复收藏被拒');
  DELETE FROM favorites WHERE user_id=1 AND post_id=1001;
  PERFORM _assert_rejects($q$INSERT INTO follows (follower_id,following_id) VALUES (1,2); INSERT INTO follows (follower_id,following_id) VALUES (1,2)$q$,
    ARRAY['23505'], '重复关注被拒');
  DELETE FROM follows WHERE follower_id=1 AND following_id=2;
  PERFORM _assert_rejects($q$INSERT INTO post_tags (post_id,tag_id) VALUES (1001,302)$q$,
    ARRAY['23505'], '重复标签关联被拒');
  PERFORM _assert_rejects($q$INSERT INTO post_images (post_id,position,image_asset_id) VALUES (1001,1,101)$q$,
    ARRAY['23505'], '同一帖子不得重复引用同一张图');
  -- 关联表确实没有挂禁删触发器：删得掉才是对的
  PERFORM _assert((SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
                    WHERE NOT t.tgisinternal AND t.tgname LIKE '%forbid_delete%'
                      AND c.relname IN ('post_likes','comment_likes','favorites','follows',
                                        'post_tags','post_flavors','post_images','comment_mentions')) = 0,
                  '关联表未挂禁删触发器（可物理增删）');
END $$;

\echo ''
-- 图片资产：删除关联 → 退役 → 重建关联 → 复活
DO $$ BEGIN
  DELETE FROM post_images WHERE post_id=1001 AND position=0;
  PERFORM _assert((SELECT status FROM image_assets WHERE id=101)='retired', '解除引用后资产退役');
  INSERT INTO post_images (post_id,position,image_asset_id) VALUES (1001,0,101);
  PERFORM _assert((SELECT status FROM image_assets WHERE id=101)='ready', '重新引用后资产复活为 ready');
  PERFORM _assert_rejects($q$UPDATE post_images SET image_asset_id=102 WHERE post_id=1001 AND position=0$q$,
    ARRAY['23001'], '关联键不可 UPDATE（换图须删除后重建）');
END $$;

-- 头像资产：换绑时激活新引用，并在最后引用解除后退役旧资产
DO $$ BEGIN
  UPDATE users SET avatar_image_asset_id=103 WHERE id=1;
  PERFORM _assert((SELECT status FROM image_assets WHERE id=102)='retired', '头像换绑后旧资产退役');
  PERFORM _assert((SELECT status FROM image_assets WHERE id=103)='ready', 'pending 头像建立引用后激活为 ready');
  UPDATE users SET avatar_image_asset_id=102 WHERE id=1;
  PERFORM _assert((SELECT status FROM image_assets WHERE id=103)='retired', '头像反向换绑后新旧状态收敛');
  PERFORM _assert((SELECT status FROM image_assets WHERE id=102)='ready', '退役头像重新引用后复活为 ready');
END $$;

\echo '########## 1d-2. 封禁：限时 / 永久 / 自动到期 ##########'
DO $$ BEGIN
  PERFORM _assert_rejects($q$UPDATE users SET banned_until=now()+interval '7 days' WHERE id=2$q$,
    ARRAY['23514'], '封禁必须同时写理由');
  PERFORM _assert_rejects($q$UPDATE users SET ban_reason='刷屏' WHERE id=2$q$,
    ARRAY['23514'], '有理由却没封禁时间同样被拒');
  PERFORM _assert_rejects($q$UPDATE users SET banned_by=3 WHERE id=2$q$,
    ARRAY['23514'], '未封禁不得单独填 banned_by');
  PERFORM _assert_rejects($q$UPDATE users SET banned_until=now()+interval '1 day', ban_reason='   ', banned_by=3 WHERE id=2$q$,
    ARRAY['23514'], '封禁理由不接受空白串');
  PERFORM _assert_rejects($q$UPDATE users SET ban_is_permanent=true, banned_until=now()+interval '1 day', ban_reason='x', banned_by=3 WHERE id=2$q$,
    ARRAY['23514'], '永久封禁不得同时带到期时间');
  PERFORM _assert_rejects($q$UPDATE users SET ban_is_permanent=true WHERE id=2$q$,
    ARRAY['23514'], '永久封禁同样必须带理由与执行人');
END $$;
-- 限时封禁 7 天
UPDATE users SET banned_until=now()+interval '7 days', ban_reason='发布无关内容', banned_by=3 WHERE id=2;
INSERT INTO user_ban_records
  (user_id,action,ban_is_permanent,banned_until,reason,actor_id)
SELECT id,'ban',ban_is_permanent,banned_until,ban_reason,banned_by FROM users WHERE id=2;
DO $$ BEGIN
  PERFORM _assert_rejects($q$DELETE FROM users WHERE id=3$q$,
    ARRAY['23001','23503'], '执行过封禁的管理员不可删除（留痕不能丢）');
END $$;
-- 永久封禁：ban_is_permanent=true 且 banned_until 必须为空
INSERT INTO users (id,email,password_hash,name,ban_is_permanent,ban_reason,banned_by)
 VALUES (8,'spam@fdueat.com','$2b$12$x','spam',true,'违法内容',3);
-- 已过期的封禁：应自动视为解封，不需要定时任务
INSERT INTO users (id,email,password_hash,name,banned_until,ban_reason,banned_by)
 VALUES (7,'served@fdueat.com','$2b$12$x','served',now()-interval '1 day','刷屏',3);
DO $$ BEGIN
  PERFORM _assert((SELECT ban_is_permanent OR banned_until > now() FROM users WHERE id=2), '限时封禁生效中');
  PERFORM _assert((SELECT ban_is_permanent AND banned_until IS NULL FROM users WHERE id=8), '永久封禁用显式布尔表达，不用 infinity');
  PERFORM _assert(NOT (SELECT ban_is_permanent OR coalesce(banned_until > now(), false) FROM users WHERE id=7), '过期封禁自动视为解封');
  PERFORM _assert(NOT (SELECT ban_is_permanent OR coalesce(banned_until > now(), false) FROM users WHERE id=1), '未封禁用户正常');
  PERFORM _assert((SELECT count(*) FROM users WHERE ban_is_permanent OR banned_until > now())=2, '当前生效封禁 2 人');
END $$;
-- 解封：所有封禁字段一起清空
UPDATE users SET ban_is_permanent=false, banned_until=NULL, ban_reason=NULL, banned_by=NULL WHERE id=2;
INSERT INTO user_ban_records (user_id,action,actor_id) VALUES (2,'unban',3);
DO $$ BEGIN
  PERFORM _assert(NOT (SELECT ban_is_permanent OR coalesce(banned_until > now(), false) FROM users WHERE id=2), '解封清空封禁字段');
END $$;
UPDATE users SET ban_is_permanent=true, ban_reason='再次违规', banned_by=3 WHERE id=2;
INSERT INTO user_ban_records
  (user_id,action,ban_is_permanent,banned_until,reason,actor_id)
SELECT id,'ban',ban_is_permanent,banned_until,ban_reason,banned_by FROM users WHERE id=2;
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM user_ban_records WHERE user_id=2)=3,
                  '封禁、解封、再封禁三次动作完整留痕');
  PERFORM _assert((SELECT reason FROM user_ban_records WHERE user_id=2 ORDER BY id LIMIT 1)='发布无关内容',
                  '第一次封禁理由永久保留');
  PERFORM _assert_rejects($q$UPDATE user_ban_records SET reason='篡改' WHERE user_id=2$q$,
    ARRAY['23001'], 'user_ban_records 禁止 UPDATE');
  PERFORM _assert_rejects($q$DELETE FROM user_ban_records WHERE user_id=2$q$,
    ARRAY['23001'], 'user_ban_records 禁止 DELETE');
END $$;

\echo ''
\echo '########## 1d-3. 评论软删除与计数 ##########'
DO $$ BEGIN
  PERFORM _assert((SELECT reply_count FROM comments WHERE id=2001)=3, '软删除前整层楼 3 条回复');
END $$;
UPDATE comments SET deleted_at=now(), deleted_reason='author' WHERE id=2003;
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM comments WHERE id=2003)=1, '软删除保留行，回复链不断');
  PERFORM _assert((SELECT count(*) FROM comments WHERE id=2004)=1, '挂在被删评论下的回复没有被连带删除');
  PERFORM _assert((SELECT content FROM comments WHERE id=2003) IS NOT NULL, '原文保留供管理员复核');
  PERFORM _assert((SELECT reply_count FROM comments WHERE id=2001)=2, '软删除后整层楼计数 -1');
END $$;
-- 恢复误杀
UPDATE comments SET deleted_at=NULL, deleted_reason=NULL WHERE id=2003;
DO $$ BEGIN
  PERFORM _assert((SELECT reply_count FROM comments WHERE id=2001)=3, '恢复后计数回到 3');
END $$;
UPDATE comments SET deleted_at=now(), deleted_reason='moderation' WHERE id=2003;

\echo ''
\echo '########## 1d-4. moderation_records 内容审核 ##########'
DO $$ BEGIN
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,comment_id,content_revision,scene,provider,verdict) VALUES (1001,2001,2,'text','tencent_ci','pass')$q$,
    ARRAY['23514'], '审核对象只能三选一');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (scene,provider,verdict) VALUES ('text','tencent_ci','pass')$q$,
    ARRAY['23514'], '审核对象不能为空');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,content_revision,scene,provider,verdict) VALUES (1001,2,'text','tencent_ci','maybe')$q$,
    ARRAY['23514'], 'verdict 枚举收敛');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (image_asset_id,scene,provider,verdict) VALUES (101,'text','tencent_ci','pass')$q$,
    ARRAY['23514'], '图片只能走图像审核');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (comment_id,content_revision,scene,provider,verdict) VALUES (2001,1,'image','tencent_ci','pass')$q$,
    ARRAY['23514'], '评论只有文本审核');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,content_revision,scene,provider,verdict,score) VALUES (1001,2,'text','tencent_ci','pass',101)$q$,
    ARRAY['23514'], 'score 取值 0-100');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,content_revision,scene,provider,verdict,reviewer_id) VALUES (1001,2,'text','manual','pass',3)$q$,
    ARRAY['23001','23514'], '人工复核记录必须成形（复核时间 + supersedes_id）');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,content_revision,scene,provider,verdict) VALUES (1001,2,'image','tencent_ci','pass')$q$,
    ARRAY['23514'], '帖子只做文本审核，图片是独立审核对象');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,content_revision,scene,provider,verdict) VALUES (1001,2,'text','  ','pass')$q$,
    ARRAY['23514'], 'provider 不接受空白串');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,content_revision,scene,provider,provider_job_id,verdict) VALUES (1001,2,'text','tencent_ci','  ','pass')$q$,
    ARRAY['23514'], '外部任务号不接受空白串');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,content_revision,scene,provider,verdict,reviewer_id,reviewed_at) VALUES (1001,2,'text','tencent_ci','pass',3,now())$q$,
    ARRAY['23514'], '机审记录不得携带人工复核信息');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,content_revision,scene,provider,provider_job_id,verdict,reviewer_id,reviewed_at) VALUES (1001,2,'text','manual','j','pass',3,now())$q$,
    ARRAY['23001','23514'], '人工记录不得带外部任务号');
END $$;
INSERT INTO moderation_records (id,post_id,content_revision,scene,provider,provider_job_id,verdict,labels,score)
 VALUES (601,1001,2,'text','tencent_ci','job-001','pass','{}',2.5);
INSERT INTO moderation_records (id,comment_id,content_revision,scene,provider,provider_job_id,verdict,labels,score)
 VALUES (602,2003,1,'text','tencent_ci','job-002','block','{politics,illegal}',97.0);
INSERT INTO moderation_records (id,image_asset_id,scene,provider,provider_job_id,verdict,labels,score)
 VALUES (603,101,'image','tencent_ci','job-003','review','{off_topic}',61.0);
DO $$ BEGIN
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,content_revision,scene,provider,provider_job_id,verdict) VALUES (1001,2,'text','tencent_ci','job-001','pass')$q$,
    ARRAY['23505'], '同一供应商任务号幂等，重复回调不写重复行');
  PERFORM _assert((SELECT count(*) FROM moderation_records WHERE labels @> ARRAY['politics'])=1, '可按违规标签检索');
  PERFORM _assert((SELECT count(*) FROM moderation_records WHERE verdict='review' AND reviewed_at IS NULL)=1, '待人工复核队列');
END $$;
-- 图片审核结论写回资产
UPDATE image_assets SET moderation='review' WHERE id=101;
DO $$ BEGIN
  PERFORM _assert_rejects($q$UPDATE image_assets SET moderation='dunno' WHERE id=101$q$,
    ARRAY['23514'], 'image_assets.moderation 枚举收敛');
  PERFORM _assert((SELECT moderation FROM image_assets WHERE id=102)='pending', '新上传图片默认待审');
END $$;
UPDATE image_assets SET moderation='pass' WHERE id IN (101,102);
-- 人工复核：新插一行，绝不覆盖机审行
DO $$ BEGIN
  PERFORM _assert_rejects($q$UPDATE moderation_records SET verdict='pass' WHERE id=603$q$,
    ARRAY['23001'], '机审行不可修改（不覆盖写由触发器强制）');
END $$;
INSERT INTO moderation_records (id,image_asset_id,scene,provider,verdict,labels,reviewer_id,reviewed_at,supersedes_id)
 VALUES (604,101,'image','manual','pass','{}',3,now(),603);
DO $$ BEGIN
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (id,image_asset_id,scene,provider,verdict,reviewer_id,reviewed_at,supersedes_id) VALUES (605,101,'image','manual','block',3,now(),603)$q$,
    ARRAY['23505'], '一条机审记录至多被复核一次');
  PERFORM _assert((SELECT verdict FROM moderation_records WHERE id=603)='review', '机审「曾判 review」这一事实被保留');
  PERFORM _assert((SELECT verdict FROM moderation_records WHERE id=604)='pass', '人工结论另起一行');
  PERFORM _assert((SELECT count(*) FROM moderation_records)=4, '复核后流水变 4 行，而不是覆盖成 3 行');
  -- 待复核队列 = 机审判 review 且没有任何人工行 supersede 它
  PERFORM _assert((SELECT count(*) FROM moderation_records m WHERE m.verdict='review' AND m.provider<>'manual'
                    AND NOT EXISTS (SELECT 1 FROM moderation_records x WHERE x.supersedes_id=m.id))=0,
                  '复核后待办队列清空');
END $$;

\echo ''
\echo '########## 1d-4a. 图片访问 durable outbox ##########'
DO $$ BEGIN
  PERFORM _assert_rejects($q$INSERT INTO image_access_intents
    (image_asset_id,source_moderation_record_id,desired_public)
    VALUES (101,603,true)$q$, ARRAY['23514'], 'review 意图不得要求公开');
END $$;
INSERT INTO image_access_intents
  (id,image_asset_id,source_moderation_record_id,desired_public)
VALUES (701,101,604,true);
INSERT INTO image_access_deliveries
  (image_asset_id,desired_intent_id,desired_public,purge_required,state,next_attempt_at)
VALUES (101,701,true,false,'pending_acl',now());
DO $$ BEGIN
  PERFORM _assert((SELECT purge_required FROM image_access_deliveries WHERE image_asset_id=101)=false,
                  '无需刷新时 delivery 仍保留并先等待 ACL');
  PERFORM _assert_rejects($q$UPDATE image_access_intents SET desired_public=false WHERE id=701$q$,
    ARRAY['55000'], '访问 intent 不可覆盖');
  PERFORM _assert_rejects($q$INSERT INTO image_access_deliveries
    (image_asset_id,desired_intent_id,desired_public,state,next_attempt_at)
    VALUES (102,701,true,'pending_acl',now())$q$, ARRAY['23505'], '同一 intent 只能投影一次');
  PERFORM _assert_rejects($q$UPDATE image_access_deliveries
    SET desired_public=false WHERE image_asset_id=101$q$,
    ARRAY['23503'], 'delivery 公开期望必须与不可变 intent 一致');
  PERFORM _assert_rejects($q$UPDATE image_access_deliveries
    SET image_asset_id=102 WHERE image_asset_id=101$q$,
    ARRAY['23503'], 'delivery 与 intent 必须属于同一图片');
  PERFORM _assert_rejects($q$UPDATE image_access_deliveries
    SET state='submitted',provider_job_id=NULL WHERE image_asset_id=101$q$,
    ARRAY['23514'], 'submitted 必须持有 provider JobId');
  PERFORM _assert_rejects($q$UPDATE image_access_deliveries
    SET state='pending_submit' WHERE image_asset_id=101$q$,
    ARRAY['23514'], '无需刷新 CDN 的 delivery 不得进入提交状态');
END $$;

\echo ''
\echo '########## 1d-4b. 图片送审失败补审队列 ##########'
INSERT INTO image_moderation_retries
  (image_asset_id,state,attempts,next_attempt_at,last_error_code)
VALUES (101,'pending',1,now(),'submit_failed');
DO $$ BEGIN
  PERFORM _assert_rejects($q$UPDATE image_moderation_retries
    SET attempts=0 WHERE image_asset_id=101$q$,
    ARRAY['23514'], '补审尝试次数必须为正数');
  PERFORM _assert_rejects($q$UPDATE image_moderation_retries
    SET state='dead_letter' WHERE image_asset_id=101$q$,
    ARRAY['23514'], '死信必须清空下次尝试并记录时间');
END $$;
UPDATE image_moderation_retries
   SET state='dead_letter',next_attempt_at=NULL,dead_lettered_at=now(),
       last_error_code='submit_exhausted'
 WHERE image_asset_id=101;
DO $$ BEGIN
  PERFORM _assert((SELECT state='dead_letter' AND attempts=1
                     FROM image_moderation_retries WHERE image_asset_id=101),
                  '补审预算耗尽后保留可观测死信');
END $$;
DELETE FROM image_moderation_retries WHERE image_asset_id=101;

-- 管理员误杀恢复不是人工复核，不 supersede 机审行，但必须能按实际操作人列检索。
INSERT INTO moderation_records
  (id,post_id,content_revision,scene,provider,verdict,labels,raw_response,reviewer_id,reviewed_at)
VALUES
  (605,1001,2,'text','admin_restore','pass','{}','{"action":"restore"}',3,now());
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM moderation_records WHERE id=605 AND reviewer_id=3)=1,
                  '恢复流水可按 reviewer_id 检索');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records
    (post_id,content_revision,scene,provider,verdict,reviewer_id,reviewed_at,supersedes_id)
    VALUES (1001,2,'text','admin_restore','pass',3,now(),603)$q$,
    ARRAY['23001','23514'], '恢复不是人工复核，不得 supersede 机审行');
END $$;

-- 管理员下架使图片失去最后一条公开帖子引用时，追加独立 block 流水并要求转私有、刷新缓存。
INSERT INTO moderation_records
  (id,image_asset_id,scene,provider,verdict,labels,raw_response,reviewer_id,reviewed_at)
VALUES
  (606,102,'image','admin_post_delete','block','{}',
   '{"action":"admin_delete_post","post_id":1001}',3,now());
UPDATE image_assets SET moderation='block' WHERE id=102;
INSERT INTO image_access_intents
  (id,image_asset_id,source_moderation_record_id,desired_public)
VALUES (702,102,606,false);
INSERT INTO image_access_deliveries
  (image_asset_id,desired_intent_id,desired_public,purge_required,state,next_attempt_at)
VALUES (102,702,false,true,'pending_acl',now());
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM moderation_records
                    WHERE id=606 AND reviewer_id=3 AND supersedes_id IS NULL)=1,
                  '管理员下架图片流水可按操作人检索且不 supersede 机审');
  PERFORM _assert((SELECT desired_public=false AND purge_required
                     FROM image_access_deliveries WHERE image_asset_id=102),
                  '管理员下架图片必须转私有并刷新 CDN');
END $$;

\echo ''
\echo '########## 1d-5. 编辑历史快照 ##########'
DO $$ BEGIN
	PERFORM _assert_rejects($q$INSERT INTO post_histories (post_id,revision,edited_by,snapshot) VALUES (1001,3,1,'"not an object"'::jsonb)$q$,
    ARRAY['23514'], '帖子快照必须是 JSON 对象');
	PERFORM _assert_rejects($q$INSERT INTO post_histories (post_id,revision,edited_by,snapshot) VALUES (1001,3,1,'[1,2]'::jsonb)$q$,
    ARRAY['23514'], '数组不算合法快照');
  PERFORM _assert((SELECT snapshot->>'title' FROM post_histories WHERE post_id=1001 AND revision=1)='测试（编辑前）',
				  '帖子历史保留旧版本');
  PERFORM _assert((SELECT snapshot->>'title' FROM post_histories WHERE post_id=1001 AND revision=2)='测试',
				  '帖子历史同时保存当前版本');
  PERFORM _assert((SELECT content FROM comment_histories WHERE comment_id=2001 AND revision=1)='楼主评论（编辑前）',
				  '评论历史保留旧正文');
  PERFORM _assert((SELECT content FROM comment_histories WHERE comment_id=2001 AND revision=2)='楼主评论',
				  '评论历史同时保存当前正文');
  PERFORM _assert((SELECT title FROM posts WHERE id=1001)='测试', '帖子主表保存当前版本');
  PERFORM _assert((SELECT content FROM comments WHERE id=2001)='楼主评论', '评论主表保存当前版本');
  PERFORM _assert(NOT EXISTS (
    SELECT 1 FROM information_schema.columns
     WHERE table_schema='public' AND table_name='moderation_records'
       AND column_name IN ('post_history_id','comment_history_id')
  ), '审核流水不再包含内容历史锚定列');
  PERFORM _assert((SELECT content_revision FROM moderation_records WHERE id=601)=2,
                  '帖子审核以整数版本号和复合外键绑定被审历史版本');
  PERFORM _assert((SELECT content_revision FROM moderation_records WHERE id=602)=1,
                  '评论创建版本与审核都从 revision 1 开始');
  PERFORM _assert((SELECT current_revision FROM posts WHERE id=1001)=2,
				  '帖子主表指针指向当前历史版本');
  PERFORM _assert((SELECT current_revision FROM comments WHERE id=2001)=2,
				  '评论主表指针指向当前历史版本');
  PERFORM _assert_rejects($q$DELETE FROM users WHERE id=1$q$, ARRAY['23001'], '编辑过内容的用户仍受软删除防线保护');
  -- 计数列只读
  PERFORM _assert_rejects($q$UPDATE posts SET comment_count=99 WHERE id=1001$q$,
    ARRAY['23001'], '计数列禁止业务代码直接写');
  -- 字典字符串规范化
  PERFORM _assert_rejects($q$INSERT INTO canteens (code,name,campus) VALUES ('x',' 空格 ','邯郸')$q$,
    ARRAY['23514'], '餐厅名必须已 trim');
  PERFORM _assert_rejects($q$INSERT INTO cuisines (name) VALUES ('   ')$q$,
    ARRAY['23514'], '菜系名不接受空白');
  PERFORM _assert_rejects($q$INSERT INTO flavors (name) VALUES (' 麻 ')$q$,
    ARRAY['23514'], '口味名必须已 trim');
  PERFORM _assert_rejects($q$INSERT INTO canteen_windows (canteen_id,name,floor) VALUES ((SELECT id FROM canteens WHERE code='canteen-nanqu'),'w',' 1F ')$q$,
    ARRAY['23514'], '楼层必须已 trim（否则绕过唯一约束）');
  PERFORM _assert_rejects($q$INSERT INTO dictionary_suggestions (kind,proposed_name,proposer_id) VALUES ('cuisine',' 新 ',1)$q$,
    ARRAY['23514'], '提议名必须已 trim');
  -- 帖子类型语义
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,status,category,title,content,price) VALUES (1,'seeking','approved','food','t','c',9.9)$q$,
    ARRAY['23514'], '提问帖不得有人均价格');
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,share_type,status,category,title,content,budget_min,budget_max) VALUES (1,'share','recommend','approved','food','t','c',1,2)$q$,
    ARRAY['23514'], '分享帖不得有预算区间');
  PERFORM _assert_rejects($q$INSERT INTO post_flavors (post_id,flavor_id,stance,post_type) VALUES (1001,(SELECT id FROM flavors WHERE name='清淡'),'prefer','share')$q$,
    ARRAY['23514'], '分享帖的口味立场只能是 has');
  PERFORM _assert_rejects($q$INSERT INTO post_flavors (post_id,flavor_id,stance,post_type) VALUES (1001,(SELECT id FROM flavors WHERE name='清淡'),'prefer','seeking')$q$,
    ARRAY['23503'], '口味立场不能靠谎报帖子类型绕过');
  -- 通知预览与类型匹配
  PERFORM _assert_rejects($q$INSERT INTO notifications (recipient_id,sender_id,type,related_post_id) VALUES (1,2,'comment',1001)$q$,
    ARRAY['23514'], 'comment 通知必须带正文预览');
  PERFORM _assert_rejects($q$INSERT INTO notifications (recipient_id,sender_id,type,content) VALUES (1,2,'follow','x')$q$,
    ARRAY['23514'], 'follow 通知不得带正文预览');
  -- 回复对象
  PERFORM _assert_rejects($q$INSERT INTO comments (post_id,author_id,reply_to_user_id,content) VALUES (1001,2,3,'x')$q$,
    ARRAY['23503'], '楼主评论的回复对象必须是帖子作者');
  PERFORM _assert_rejects($q$INSERT INTO comments (post_id,author_id,parent_id,root_id,reply_to_user_id,content) VALUES (1001,1,2001,2001,3,'x')$q$,
    ARRAY['23503'], '回复的回复对象必须是父评论作者');
END $$;

\echo '-- 追加不可篡改：审核流水与编辑历史'
DO $$ BEGIN
  PERFORM _assert_rejects($q$UPDATE post_histories SET snapshot='{"t":1}'::jsonb WHERE id=1$q$,
    ARRAY['23001'], '编辑历史不可修改');
  PERFORM _assert_rejects($q$DELETE FROM post_histories WHERE id=1$q$,
    ARRAY['23001'], '编辑历史不可删除');
  PERFORM _assert_rejects($q$DELETE FROM comment_histories WHERE comment_id=2001$q$,
    ARRAY['23001'], '评论历史不可删除');
  PERFORM _assert_rejects($q$DELETE FROM moderation_records WHERE id=601$q$,
    ARRAY['23001'], '审核流水不可删除');
  PERFORM _assert_rejects($q$INSERT INTO post_histories (post_id,revision,edited_by,snapshot) VALUES (1001,1,1,'{}'::jsonb)$q$,
    ARRAY['23505'], '同一帖子的版本号唯一');
  -- 人工复核关系闭合
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,content_revision,scene,provider,verdict,reviewer_id,reviewed_at) VALUES (1001,2,'text','manual','pass',3,now())$q$,
    ARRAY['23001'], '人工复核必须带 supersedes_id');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,content_revision,scene,provider,verdict,supersedes_id) VALUES (1001,2,'text','ci','pass',601)$q$,
    ARRAY['23514','23001'], '机审记录不得带 supersedes_id');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,content_revision,scene,provider,verdict,reviewer_id,reviewed_at,supersedes_id) VALUES (1001,2,'text','manual','pass',3,now(),601)$q$,
    ARRAY['23001'], '只能复核机审未通过的 review 或 block 记录');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (comment_id,content_revision,scene,provider,verdict,reviewer_id,reviewed_at,supersedes_id) VALUES (2001,2,'text','manual','pass',3,now(),603)$q$,
    ARRAY['23001'], '人工复核必须针对同一对象');
  -- 计数器：INSERT 灌值 + GUC 不泄漏
  PERFORM _assert_rejects($q$INSERT INTO posts (author_id,post_type,share_type,status,category,title,content,like_count) VALUES (1,'share','recommend','approved','food','t','c',999)$q$,
    ARRAY['23001'], '新建帖子不得直接灌入计数');
  PERFORM _assert_rejects($q$INSERT INTO comments (post_id,author_id,reply_to_user_id,content,reply_count) VALUES (1001,1,1,'x',5)$q$,
    ARRAY['23001'], '新建评论不得直接灌入计数');
END $$;
-- 逃生阀不得泄漏到同事务后续语句
DO $$
DECLARE leaked boolean;
BEGIN
  INSERT INTO post_likes (user_id,post_id) VALUES (3,1001);
  BEGIN
    UPDATE posts SET like_count=777 WHERE id=1001;
    leaked := true;
  EXCEPTION WHEN others THEN
    leaked := false;
  END;
  PERFORM _assert(NOT leaked, '同步触发器的逃生阀不泄漏到同事务后续写入');
  DELETE FROM post_likes WHERE user_id=3 AND post_id=1001;
END $$;


\echo ''


\echo ''
\echo '########## 1d-6. 审核覆盖：标签与用户昵称 ##########'
DO $$ BEGIN
  PERFORM _assert((SELECT moderation FROM tags WHERE id=301)='pending', '新建标签默认待审（先发后审）');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (user_id,scene,provider,verdict) VALUES (1,'text','tencent_ci','pass')$q$,
    ARRAY['23514'], '用户对象必须指明审的是昵称还是简介');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (tag_id,scene,provider,verdict) VALUES (301,'image','tencent_ci','pass')$q$,
    ARRAY['23514'], '标签只做文本审核');
  PERFORM _assert_rejects($q$INSERT INTO moderation_records (post_id,tag_id,content_revision,scene,provider,verdict) VALUES (1001,301,2,'text','tencent_ci','pass')$q$,
    ARRAY['23514'], '审核对象五选一');
END $$;
INSERT INTO moderation_records (id,tag_id,scene,provider,provider_job_id,verdict,labels)
 VALUES (610,301,'text','tencent_ci','job-tag-1','block','{ad}');
INSERT INTO moderation_records (id,user_id,field,scene,provider,provider_job_id,verdict,labels)
 VALUES (611,2,'name','text','tencent_ci','job-user-1','review','{off_topic}');
DO $$ BEGIN
  PERFORM _assert_rejects($q$INSERT INTO moderation_records
    (user_id,field,scene,provider,verdict,reviewer_id,reviewed_at,supersedes_id)
    VALUES (2,'bio','text','manual','pass',3,now(),611)$q$,
    ARRAY['23001'], '人工复核必须针对同一对象的同一字段');
END $$;
-- 标签机审判违规 → 下架，但关联行保留以便误杀恢复
UPDATE tags SET moderation='block', deleted_at=now() WHERE id=301;
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM post_tags WHERE tag_id=301)=1, '标签下架后关联行保留（可恢复）');
  PERFORM _assert((SELECT count(*) FROM tags WHERE id=301 AND deleted_at IS NOT NULL)=1, '违规标签被下架');
  PERFORM _assert((SELECT count(*) FROM moderation_records WHERE user_id IS NOT NULL AND field='name')=1, '昵称可作为审核对象');
END $$;
-- 恢复误杀
UPDATE tags SET moderation='pass', deleted_at=NULL WHERE id=301;

\echo ''
\echo '########## 1d-7. 审核积压告警抑制状态 ##########'
INSERT INTO moderation_alert_states
  (alert_key,active,last_observed_count,last_alerted_at,updated_at)
VALUES ('review_backlog',true,12,now(),now());
DO $$ BEGIN
  PERFORM _assert((SELECT last_observed_count FROM moderation_alert_states
                    WHERE alert_key='review_backlog')=12,
                  '审核积压告警状态可跨周期任务保存');
  PERFORM _assert_rejects($q$INSERT INTO moderation_alert_states
    (alert_key,last_observed_count) VALUES ('future_alert',0)$q$,
    ARRAY['23514'], '告警状态键不预留未决定的类型');
  PERFORM _assert_rejects($q$UPDATE moderation_alert_states
    SET last_observed_count=-1 WHERE alert_key='review_backlog'$q$,
    ARRAY['23514'], '审核积压计数不得为负');
  PERFORM _assert_rejects($q$UPDATE moderation_alert_states
    SET active=true,last_alerted_at=NULL WHERE alert_key='review_backlog'$q$,
    ARRAY['23514'], '活跃告警状态必须记录最近告警时间');
END $$;
UPDATE moderation_alert_states
SET active=false,last_observed_count=0,updated_at=now()
WHERE alert_key='review_backlog';
DO $$ BEGIN
  PERFORM _assert((SELECT NOT active AND last_observed_count=0
                    FROM moderation_alert_states WHERE alert_key='review_backlog'),
                  '积压回落后状态可重置且保留历史告警时间');
END $$;

\echo ''
\echo '########## 1e. notifications：type 与关联目标必须匹配 ##########'
DO $$ BEGIN
  PERFORM _assert_rejects($q$INSERT INTO notifications (recipient_id,sender_id,type,related_post_id,related_comment_id) VALUES (1,2,'comment',1001,2001)$q$,
    ARRAY['23514'], 'comment 通知不得同时关联评论');
  PERFORM _assert_rejects($q$INSERT INTO notifications (recipient_id,sender_id,type,related_comment_id) VALUES (1,2,'comment',2001)$q$,
    ARRAY['23514'], 'comment 通知必须关联帖子');
  PERFORM _assert_rejects($q$INSERT INTO notifications (recipient_id,sender_id,type,related_post_id) VALUES (1,2,'reply',1001)$q$,
    ARRAY['23514'], 'reply 通知必须关联评论而非帖子');
  PERFORM _assert_rejects($q$INSERT INTO notifications (recipient_id,sender_id,type,related_post_id) VALUES (1,2,'follow',1001)$q$,
    ARRAY['23514'], 'follow 通知不得有关联目标');
  PERFORM _assert_rejects($q$INSERT INTO notifications (recipient_id,sender_id,type,related_comment_id) VALUES (1,2,'shout',2001)$q$,
    ARRAY['23514'], 'notifications.type 枚举收敛');
END $$;
-- 正例：五种类型都能落库，mention 必须被接受（旧实现会创建这一类）
INSERT INTO notifications (recipient_id,sender_id,type,related_post_id,content) VALUES (1,2,'like_post',1001,NULL);
INSERT INTO notifications (recipient_id,sender_id,type,related_post_id,content) VALUES (1,2,'comment',1001,'评论正文预览');
INSERT INTO notifications (recipient_id,sender_id,type,related_comment_id,content) VALUES (1,2,'reply',2001,'回复正文预览');
INSERT INTO notifications (recipient_id,sender_id,type,related_comment_id,content) VALUES (1,2,'mention',2001,'@ 正文预览');
INSERT INTO notifications (recipient_id,sender_id,type,related_comment_id) VALUES (1,2,'like_comment',2001);
INSERT INTO notifications (recipient_id,sender_id,type) VALUES (1,2,'follow');
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM notifications)=6, '六类通知均可落库');
  PERFORM _assert((SELECT count(*) FROM notifications WHERE type='mention')=1, 'mention 类型被接受（不得回归）');
  PERFORM _assert((SELECT count(*) FROM notifications WHERE content IS NOT NULL)=3, 'content 为活字段，三类通知写入预览');
END $$;

\echo ''
\echo '########## 1f. email_verification_codes ##########'
INSERT INTO email_verification_codes (email,code_digest,expires_at,send_window_started_at)
 VALUES ('Foo@fdueat.com','d1',now()+interval '10 min',now());
DO $$ BEGIN
  PERFORM _assert_rejects($q$INSERT INTO email_verification_codes (email,code_digest,expires_at,send_window_started_at) VALUES ('foo@fdueat.com','d2',now()+interval '10 min',now())$q$,
    ARRAY['23505'], 'evc (lower(email), purpose) 唯一');
  PERFORM _assert_rejects($q$INSERT INTO email_verification_codes (email,code_digest,expires_at,send_window_started_at,send_count) VALUES ('bar@fdueat.com','d3',now()+interval '10 min',now(),-1)$q$,
    ARRAY['23514'], 'evc 计数器不得为负');
END $$;

\echo ''
\echo '########## 1g. user_sessions 会话与撤销 ##########'
INSERT INTO user_sessions (id,user_id,refresh_token_digest,device_label,ip,expires_at) VALUES
 (501,1,repeat('a',64),'alice-iphone','203.0.113.9', now()+interval '30 days'),
 (502,1,repeat('b',64),'alice-ipad','203.0.113.10',now()+interval '30 days'),
 (503,2,repeat('c',64),'bob-android','203.0.113.11',now()+interval '30 days');
-- 已过期会话：用 created_at 回拨保证满足 expires_at > created_at
INSERT INTO user_sessions (id,user_id,refresh_token_digest,device_label,expires_at,created_at,last_seen_at)
 VALUES (504,2,repeat('d',64),'bob-old-phone',now()-interval '1 day',now()-interval '40 days',now()-interval '2 days');
DO $$ BEGIN
  PERFORM _assert_rejects($q$INSERT INTO user_sessions (user_id,refresh_token_digest,expires_at) VALUES (1,repeat('a',64),now()+interval '1 day')$q$,
    ARRAY['23505'], 'refresh 摘要全局唯一');
  PERFORM _assert_rejects($q$INSERT INTO user_sessions (user_id,refresh_token_digest,expires_at) VALUES (1,repeat('z',64),now()+interval '1 day')$q$,
    ARRAY['23514'], '摘要必须是十六进制');
  PERFORM _assert_rejects($q$INSERT INTO user_sessions (user_id,refresh_token_digest,expires_at) VALUES (1,repeat('A',64),now()+interval '1 day')$q$,
    ARRAY['23514'], '摘要拒绝大写');
  PERFORM _assert_rejects($q$INSERT INTO user_sessions (user_id,refresh_token_digest,expires_at) VALUES (1,repeat('9',64),now()-interval '1 day')$q$,
    ARRAY['23514'], 'expires_at 必须晚于 created_at');
  PERFORM _assert_rejects($q$INSERT INTO user_sessions (user_id,refresh_token_digest,expires_at,last_seen_at) VALUES (1,repeat('9',64),now()+interval '1 day',now()+interval '30 days')$q$,
    ARRAY['23514'], 'last_seen_at 不得晚于 expires_at');
  -- 鉴权查询：命中未撤销且未过期的会话
  PERFORM _assert((SELECT count(*) FROM user_sessions s JOIN users u ON u.id=s.user_id
                    WHERE s.id=501 AND s.user_id=1 AND s.revoked_at IS NULL AND s.expires_at>now()
                      AND u.deleted_at IS NULL
                      AND NOT (u.ban_is_permanent OR coalesce(u.banned_until > now(), false)))=1,
                  '鉴权查询命中有效会话');
END $$;

UPDATE user_sessions SET revoked_at=now() WHERE id=502;
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM user_sessions WHERE id=502 AND revoked_at IS NULL AND expires_at>now())=0, '本设备登出立即失效');
  PERFORM _assert((SELECT count(*) FROM user_sessions WHERE user_id=1 AND revoked_at IS NULL)=1, '登出不波及同用户其他设备');
END $$;

UPDATE user_sessions SET revoked_at=now() WHERE user_id=1 AND revoked_at IS NULL;
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM user_sessions WHERE user_id=1 AND revoked_at IS NULL)=0, '登出所有设备');
  PERFORM _assert((SELECT count(*) FROM user_sessions WHERE user_id=2 AND revoked_at IS NULL)=2, '登出不波及其他用户');
  PERFORM _assert((SELECT count(*) FROM user_sessions WHERE id=504 AND revoked_at IS NULL AND expires_at>now())=0, '过期会话不可用');
  PERFORM _assert((SELECT count(*) FROM user_sessions WHERE revoked_at IS NOT NULL)=2, '撤销是打标记，行保留');
END $$;

INSERT INTO users (id,email,password_hash,name) VALUES (9,'gone@fdueat.com','$2b$12$x','gone');
INSERT INTO user_sessions (user_id,refresh_token_digest,expires_at) VALUES (9,repeat('f',64),now()+interval '1 day');
-- 注销是软删除：账号不可登录，但内容与会话记录保留
UPDATE users SET deleted_at=now() WHERE id=9;
UPDATE user_sessions SET revoked_at=now() WHERE user_id=9;
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM users WHERE id=9 AND deleted_at IS NOT NULL)=1, '注销为软删除，行保留');
  PERFORM _assert((SELECT count(*) FROM user_sessions s JOIN users u ON u.id=s.user_id
                    WHERE s.user_id=9 AND s.revoked_at IS NULL AND u.deleted_at IS NULL)=0, '注销后无可用会话');
END $$;

\echo ''
\echo '########## 1h. dictionary_suggestions 状态机 ##########'
DO $$ BEGIN
  PERFORM _assert_rejects($q$INSERT INTO dictionary_suggestions (kind,proposed_name,proposer_id) VALUES ('flavor','孜然味',1)$q$,
    ARRAY['23514'], 'flavor 提议必须带 stance');
  PERFORM _assert_rejects($q$INSERT INTO dictionary_suggestions (kind,proposed_name,proposer_id,flavor_stance) VALUES ('cuisine','新疆菜',1,'has')$q$,
    ARRAY['23514'], '非 flavor 不得带 stance');
  PERFORM _assert_rejects($q$INSERT INTO dictionary_suggestions (kind,proposed_name,proposer_id) VALUES ('canteen_window','三楼砂锅',1)$q$,
    ARRAY['23514'], '窗口提议必须指明所属餐厅');
  PERFORM _assert_rejects($q$INSERT INTO dictionary_suggestions (kind,proposed_name,proposer_id,resulting_cuisine_id) VALUES ('cuisine','新疆菜',1,(SELECT id FROM cuisines LIMIT 1))$q$,
    ARRAY['23514'], 'pending 不得有产出');
  PERFORM _assert_rejects($q$INSERT INTO dictionary_suggestions (kind,proposed_name,proposer_id,reviewer_id) VALUES ('cuisine','新疆菜',1,3)$q$,
    ARRAY['23514'], 'pending 不得有审核人');
END $$;

INSERT INTO dictionary_suggestions (id,kind,proposed_name,proposer_id,post_id,flavor_stance)
 VALUES (401,'flavor','孜然味',1,1001,'has');
INSERT INTO dictionary_suggestions (id,kind,proposed_name,proposer_id,post_id,parent_canteen_id)
 VALUES (402,'canteen_window','三楼砂锅',2,1001,(SELECT id FROM canteens WHERE code='canteen-nanqu'));
INSERT INTO dictionary_suggestions (id,kind,proposed_name,proposer_id,post_id)
 VALUES (403,'cuisine','分子料理',2,1001);

DO $$ BEGIN
  PERFORM _assert_rejects($q$UPDATE dictionary_suggestions SET status='approved', reviewer_id=3, resulting_flavor_id=(SELECT id FROM flavors LIMIT 1) WHERE id=401$q$,
    ARRAY['23514'], '审核通过必须同时有审核人与审核时间');
  PERFORM _assert_rejects($q$UPDATE dictionary_suggestions SET status='rejected', reviewer_id=3, reviewed_at=now() WHERE id=403$q$,
    ARRAY['23514'], '驳回必须写明理由');
  -- 批出来的窗口必须属于提议时指定的餐厅（202 属江湾，402 提的是南区）
  PERFORM _assert_rejects($q$UPDATE dictionary_suggestions SET status='approved', reviewer_id=3, reviewed_at=now(), resulting_window_id=202 WHERE id=402$q$,
    ARRAY['23503'], '批出的窗口必须属于提议指定的餐厅');
END $$;

-- 正常链路：提议 → 建条目 → 回填产出 → 绑回帖子
WITH nf AS (INSERT INTO flavors (name,sort_order) VALUES ('孜然味',160) RETURNING id)
UPDATE dictionary_suggestions d SET status='approved', reviewer_id=3, reviewed_at=now(), resulting_flavor_id=nf.id
  FROM nf WHERE d.id=401;
INSERT INTO post_flavors (post_id,flavor_id,stance,post_type)
 SELECT post_id, resulting_flavor_id, flavor_stance, 'share' FROM dictionary_suggestions WHERE id=401;
WITH nw AS (INSERT INTO canteen_windows (canteen_id,name)
              SELECT parent_canteen_id,'三楼砂锅' FROM dictionary_suggestions WHERE id=402 RETURNING id)
UPDATE dictionary_suggestions d SET status='approved', reviewer_id=3, reviewed_at=now(), resulting_window_id=nw.id
  FROM nw WHERE d.id=402;
UPDATE dictionary_suggestions SET status='rejected', reviewer_id=3, reviewed_at=now(), review_note='与「其他」重复' WHERE id=403;

DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM dictionary_suggestions WHERE status='approved')=2, '两条提议通过');
  PERFORM _assert((SELECT count(*) FROM dictionary_suggestions WHERE status='rejected')=1, '一条提议驳回');
  PERFORM _assert((SELECT count(*) FROM post_flavors WHERE post_id=1001)=3, '通过的口味已绑回帖子');
  PERFORM _assert_rejects($q$UPDATE dictionary_suggestions SET resulting_flavor_id=NULL, resulting_cuisine_id=(SELECT id FROM cuisines LIMIT 1) WHERE id=401$q$,
    ARRAY['23001','23514'], '产出类型必须匹配 kind（终态保护先于 CHECK 生效）');
  -- 被审核历史引用的词条不可物理删除（RESTRICT），只能停用
  PERFORM _assert_rejects($q$DELETE FROM flavors WHERE name='孜然味'$q$,
    ARRAY['23001','23503'], '被提议引用的词条不可物理删除');
  -- 提议记录本身：不可删、终态不可回退、内容不可篡改
  PERFORM _assert_rejects($q$DELETE FROM dictionary_suggestions WHERE id=403$q$,
    ARRAY['23001'], '提议记录不可物理删除');
  PERFORM _assert_rejects($q$UPDATE dictionary_suggestions SET status='pending',reviewer_id=NULL,reviewed_at=NULL,review_note=NULL WHERE id=403$q$,
    ARRAY['23001'], '终态提议不可回退为 pending');
  PERFORM _assert_rejects($q$UPDATE dictionary_suggestions SET proposed_name='篡改' WHERE id=403$q$,
    ARRAY['23001'], '终态提议的内容不可篡改');
  -- 用 401（已批准的口味提议，resulting_flavor_id 有值）才能真的构成篡改
  PERFORM _assert_rejects($q$UPDATE dictionary_suggestions SET resulting_flavor_id=NULL WHERE id=401$q$,
    ARRAY['23001','23514'], '终态提议的产出不可篡改');
END $$;

\echo '-- 餐厅与窗口一起提议：先提餐厅，窗口挂到那条提议上'
DO $$ BEGIN
  PERFORM _assert_rejects($q$INSERT INTO dictionary_suggestions (kind,proposed_name,proposer_id,parent_suggestion_id) VALUES ('canteen_window','某窗口',1,401)$q$,
    ARRAY['23503'], '窗口提议的父提议必须是餐厅提议（401 是口味提议）');
  PERFORM _assert_rejects($q$INSERT INTO dictionary_suggestions (kind,proposed_name,proposer_id,parent_suggestion_id) VALUES ('cuisine','某菜系',1,404)$q$,
    ARRAY['23514'], '非窗口提议不得带 parent_suggestion_id');
END $$;
INSERT INTO dictionary_suggestions (id,kind,proposed_name,proposer_id) VALUES (404,'canteen','新食堂',1);
INSERT INTO dictionary_suggestions (id,kind,proposed_name,proposer_id,parent_suggestion_id)
 VALUES (405,'canteen_window','新食堂一楼窗口',1,404);
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM dictionary_suggestions WHERE id=405 AND parent_canteen_id IS NULL)=1,
                  '联合提议时窗口的 parent_canteen_id 可暂空');
  PERFORM _assert_rejects($q$UPDATE dictionary_suggestions SET status='approved',reviewer_id=3,reviewed_at=now(),resulting_window_id=201 WHERE id=405$q$,
    ARRAY['23514'], '批窗口前必须先把 parent_canteen_id 落实');
END $$;
-- 正确顺序：先批餐厅
WITH nc AS (INSERT INTO canteens (code,name,campus) VALUES ('canteen-new','新食堂','邯郸校区') RETURNING id)
UPDATE dictionary_suggestions d SET status='approved',reviewer_id=3,reviewed_at=now(),resulting_canteen_id=nc.id
  FROM nc WHERE d.id=404;
-- 再批窗口：回填 parent_canteen_id 并建窗口
UPDATE dictionary_suggestions SET parent_canteen_id=(SELECT resulting_canteen_id FROM dictionary_suggestions WHERE id=404) WHERE id=405;
WITH nw AS (INSERT INTO canteen_windows (canteen_id,name,floor)
              SELECT parent_canteen_id,'新食堂一楼窗口','1F' FROM dictionary_suggestions WHERE id=405 RETURNING id)
UPDATE dictionary_suggestions d SET status='approved',reviewer_id=3,reviewed_at=now(),resulting_window_id=nw.id
  FROM nw WHERE d.id=405;
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM dictionary_suggestions WHERE id IN (404,405) AND status='approved')=2,
                  '餐厅+窗口联合提议可完整走通');
  -- 挂错餐厅：窗口提议挂在 #404（新食堂）名下，却想批成南区食堂的窗口
  PERFORM _assert_rejects($q$INSERT INTO dictionary_suggestions (id,kind,proposed_name,proposer_id,parent_suggestion_id,parent_canteen_id) VALUES (406,'canteen_window','挂错的窗口',1,404,(SELECT id FROM canteens WHERE code='canteen-nanqu'))$q$,
    ARRAY['23503'], '窗口的父餐厅必须等于父提议批出的那个餐厅');
  -- 父提议还是 pending 就想批子提议
  PERFORM _assert_rejects($q$INSERT INTO dictionary_suggestions (id,kind,proposed_name,proposer_id) VALUES (407,'canteen','待审食堂',1) $q$ ||
    $q$; INSERT INTO dictionary_suggestions (id,kind,proposed_name,proposer_id,parent_suggestion_id,parent_canteen_id) VALUES (408,'canteen_window','抢跑窗口',1,407,(SELECT id FROM canteens WHERE code='canteen-nanqu'))$q$,
    ARRAY['23503'], '父提议未批准时子提议无法落实父餐厅');
  PERFORM _assert((SELECT w.canteen_id FROM canteen_windows w
                    JOIN dictionary_suggestions d ON d.resulting_window_id=w.id WHERE d.id=405)
                = (SELECT resulting_canteen_id FROM dictionary_suggestions WHERE id=404),
                  '批出的窗口确实挂在新建的餐厅下');
END $$;

\echo ''
\echo '########## 2. 计数器触发器 ##########'
INSERT INTO post_likes (user_id,post_id) VALUES (2,1001);
INSERT INTO favorites  (user_id,post_id) VALUES (2,1001);
INSERT INTO comment_likes (user_id,comment_id) VALUES (1,2001);
DO $$ BEGIN
  -- 此时 2003 已被机审软删除（见 1d-3），故 4 条评论只计 3 条
  PERFORM _assert((SELECT (like_count,favorite_count,comment_count) FROM posts WHERE id=1001) = (1,1,3)::record, 'posts 计数器 = (1,1,3)（软删除的不计入）');
  PERFORM _assert((SELECT (like_count,reply_count) FROM comments WHERE id=2001) = (1,2)::record, '楼主评论 reply_count = 2（整层楼未删除的回复数）');
  PERFORM _assert((SELECT reply_count FROM comments WHERE id=2002)=0, '回复行 reply_count 恒为 0');
END $$;

\echo '-- 关联键不可变：UPDATE 会绕过计数器，必须被拒绝'
DO $$ BEGIN
  PERFORM _assert_rejects($q$UPDATE post_likes SET post_id=1004 WHERE user_id=2$q$,     ARRAY['23001'], 'post_likes 关联键不可变');
  PERFORM _assert_rejects($q$UPDATE favorites SET post_id=1004 WHERE user_id=2$q$,      ARRAY['23001'], 'favorites 关联键不可变');
  PERFORM _assert_rejects($q$UPDATE comment_likes SET comment_id=2002 WHERE user_id=1$q$, ARRAY['23001'], 'comment_likes 关联键不可变');
  PERFORM _assert_rejects($q$UPDATE comments SET post_id=1004 WHERE id=2001$q$,         ARRAY['23001'], 'comments.post_id 不可变');
  PERFORM _assert_rejects($q$UPDATE comments SET parent_id=NULL, root_id=NULL WHERE id=2002$q$, ARRAY['23001'], 'comments.parent_id 不可变');
  PERFORM _assert_rejects($q$UPDATE comments SET root_id=2002 WHERE id=2003$q$,          ARRAY['23001'], 'comments.root_id 不可变');
  -- 改成同值不算变更，触发器不应误伤
  UPDATE comments SET parent_id=2001 WHERE id=2002;
  PERFORM _assert((SELECT parent_id FROM comments WHERE id=2002)=2001, '关联键写入同值不被误拦');
END $$;

\echo ''
\echo '########## 3. updated_at：无触发器，纯业务层语义 ##########'
DO $$ BEGIN
  PERFORM _assert((SELECT updated_at = created_at FROM posts WHERE id=1001), '点赞/收藏/评论不改动 updated_at');
END $$;
UPDATE posts SET view_count = view_count + 1 WHERE id=1001;
DO $$ BEGIN
  PERFORM _assert((SELECT updated_at = created_at FROM posts WHERE id=1001), 'view_count 自增不改动 updated_at');
END $$;
UPDATE posts SET content='改过的正文', updated_at=now() WHERE id=1001;
DO $$ BEGIN
  PERFORM _assert((SELECT updated_at > created_at FROM posts WHERE id=1001), '业务层显式写入才推进 updated_at');
END $$;

\echo ''
\echo '########## 4. 取消操作后计数器回落 ##########'
DELETE FROM post_likes WHERE user_id=2;
-- 删 2002 会级联删掉挂在它下面的 2003、2004（三条一起消失）
-- 软删除 2002：它下面的 2003、2004 必须原封不动
UPDATE comments SET deleted_at=now(), deleted_reason='author' WHERE id=2002;
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM comments WHERE id IN (2003,2004))=2, '软删中间回复不伤及其子回复');
  PERFORM _assert((SELECT (like_count,comment_count) FROM posts WHERE id=1001) = (0,2)::record, '取消点赞 + 软删 1 条后计数回落');
  PERFORM _assert((SELECT reply_count FROM comments WHERE id=2001)=1, '整层楼只减去被软删的那一条');
END $$;

\echo ''
\echo '########## 5. danshi_recount_all()：一致时 0 修正且不动时间戳 ##########'
CREATE TEMP TABLE _ts AS SELECT id, updated_at FROM posts;
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT * FROM danshi_recount_all() LOOP
    PERFORM _assert(r.fixed_rows = 0, format('recount 一致：%s 修正 0 行', r.table_name));
  END LOOP;
  PERFORM _assert((SELECT bool_and(p.updated_at = t.updated_at) FROM posts p JOIN _ts t USING (id)), 'recount 不污染 updated_at');
END $$;

\echo ''
\echo '########## 6. 计数器漂移可被 recount 修复 ##########'
BEGIN; SET LOCAL danshi.allow_counter_write='on'; UPDATE posts SET like_count=99 WHERE id=1001; COMMIT;
DO $$
DECLARE n bigint;
BEGIN
  SELECT fixed_rows INTO n FROM danshi_recount_all() WHERE table_name='posts';
  PERFORM _assert(n = 1, 'recount 修正 1 行漂移');
  PERFORM _assert((SELECT like_count FROM posts WHERE id=1001)=0, 'like_count 回到真实值');
END $$;

\echo ''
\echo '########## 7. 图片资产退役 ##########'
UPDATE users SET avatar_image_asset_id=NULL WHERE id=1;
DO $$ BEGIN PERFORM _assert((SELECT status FROM image_assets WHERE id=102)='retired', '头像换绑后旧资产退役'); END $$;
-- 日常删帖是软删除：关联、评论、历史全部保留
UPDATE posts SET deleted_at=now(), deleted_reason='admin', deleted_by=3 WHERE id=1001;
DO $$ BEGIN
  PERFORM _assert((SELECT count(*) FROM post_tags WHERE post_id=1001)>0, '软删帖保留标签关联');
  PERFORM _assert((SELECT count(*) FROM comments WHERE post_id=1001)>0,  '软删帖保留评论');
  PERFORM _assert((SELECT count(*) FROM dictionary_suggestions WHERE post_id=1001)>0, '软删帖保留提议来源');
END $$;
-- 运维物理清除时才级联，并触发图片资产退役
BEGIN; SET LOCAL danshi.allow_hard_delete='on'; DELETE FROM posts WHERE id=1001; COMMIT;
DO $$ BEGIN
  PERFORM _assert((SELECT status FROM image_assets WHERE id=101)='retired', '物理清除后图片资产退役');
  PERFORM _assert((SELECT count(*) FROM post_tags)=0,    '物理清除级联 post_tags');
  PERFORM _assert((SELECT count(*) FROM post_flavors)=0, '物理清除级联 post_flavors');
  PERFORM _assert((SELECT count(*) FROM tags WHERE deleted_at IS NULL)=3, 'tags 字典本身保留');
  PERFORM _assert((SELECT count(*) FROM dictionary_suggestions)=5,       '提议行保留');
  PERFORM _assert((SELECT count(post_id) FROM dictionary_suggestions)=0, '来源帖清除后 post_id 置空');
END $$;

\echo ''
\echo '########## 8. 运维物理删除通道 ##########'
-- Grandfather 上线后每张历史图片都有审核记录，purge 必须能连带清除审核流水
INSERT INTO image_assets (id,uploader_id,purpose,object_key,public_url,content_type,status)
 VALUES (150,1,'post','k150','https://img/150.jpg','image/jpeg','ready'),
        (151,1,'post','k151','https://img/151.jpg','image/jpeg','ready');
INSERT INTO moderation_records (id,image_asset_id,scene,provider,verdict,labels)
 VALUES (650,150,'image','legacy_migration','pass','{}');
DO $$ BEGIN
  PERFORM _assert(danshi_purge_image_assets(ARRAY[102]::bigint[]) = 1, 'purge 删除 1 行');
  PERFORM _assert(danshi_purge_image_assets(ARRAY[150]::bigint[]) = 1, 'purge 可清除带审核记录的资产');
  PERFORM _assert((SELECT count(*) FROM moderation_records WHERE id=650)=0, '审核流水随资产级联清除');
END $$;
-- purge 不得泄漏两个逃生阀
DO $$
DECLARE leaked boolean;
BEGIN
  PERFORM danshi_purge_image_assets(ARRAY[]::bigint[]);
  BEGIN
    DELETE FROM image_assets WHERE id=151; leaked := true;
  EXCEPTION WHEN others THEN leaked := false;
  END;
  PERFORM _assert(NOT leaked, 'purge 后 allow_image_asset_delete 不泄漏');
  BEGIN
    DELETE FROM tags WHERE id=303; leaked := true;
  EXCEPTION WHEN others THEN leaked := false;
  END;
  PERFORM _assert(NOT leaked, 'purge 后 allow_hard_delete 不泄漏');
END $$;

\echo ''
\echo '########## 9. identity 序列：显式装载后 setval（ETL 必做）##########'
DO $$
DECLARE next_id bigint;
BEGIN
  -- 非空表：setval 到 max(id)
  PERFORM setval(pg_get_serial_sequence('users','id'), (SELECT max(id) FROM users));
  INSERT INTO users (email,password_hash,name) VALUES ('next@fdueat.com','x','next_user') RETURNING id INTO next_id;
  PERFORM _assert(next_id > 3, '非空表 setval 后不撞主键');
  -- 空表：max(id) 为 NULL，必须走 setval(seq, 1, false)，直接传 NULL 会报错
  PERFORM _assert((SELECT count(*) FROM comment_mentions)=0, 'comment_mentions 为空（空表分支前提）');
  -- 真正的空表分支：comment_mentions 从头到尾没有数据
  PERFORM setval(pg_get_serial_sequence('comment_mentions','comment_id'),
                 coalesce((SELECT max(comment_id) FROM comment_mentions), 1),
                 (SELECT max(comment_id) FROM comment_mentions) IS NOT NULL);
  PERFORM _assert(true, '空表 setval 分支可执行（coalesce + is_called=false）');
END $$;

\echo ''
\echo '########## 10. 系统目录：关键索引与触发器必须存在 ##########'
DO $$
DECLARE want text; missing text[] := '{}';
BEGIN
  FOREACH want IN ARRAY ARRAY[
    'idx_posts_window_created','idx_post_tags_tag','idx_post_flavors_flavor',
    'idx_posts_status_created_id','idx_notifications_recipient_created_id',
    'idx_posts_title_trgm','idx_posts_content_trgm','idx_users_name_trgm',
    'idx_ds_pending','idx_user_sessions_active','idx_user_sessions_expires',
    'idx_user_roles_role','idx_user_ban_records_user','idx_user_role_records_user',
    'idx_image_moderation_retries_due',
    'uq_users_email_lower','uq_users_name_lower','uq_user_name_claims_name_lower',
    'uq_tags_name_lower','uq_user_sessions_digest','uq_image_assets_object_key',
    'uq_image_assets_public_url','uq_evc_email_purpose'
  ] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname=want) THEN
      missing := missing || want;
    END IF;
  END LOOP;
  PERFORM _assert(cardinality(missing)=0, format('关键索引齐全（缺失：%s）', missing));

  PERFORM _assert(EXISTS (
    SELECT 1 FROM pg_extension WHERE extname='pg_trgm'
  ), 'pg_trgm 扩展已启用');
  PERFORM _assert(EXISTS (
    SELECT 1
      FROM pg_index i
     WHERE i.indexrelid='uq_image_assets_public_url'::regclass
       AND i.indrelid='image_assets'::regclass
       AND i.indisunique AND i.indisvalid AND i.indisready
       AND i.indpred IS NOT NULL
       AND pg_get_expr(i.indpred, i.indrelid)=
           '(public_url <> ''''::text)'
  ), 'image_assets.public_url 仅对非空值使用部分唯一索引');
  PERFORM _assert(EXISTS (
    SELECT 1
      FROM pg_index i
      JOIN pg_class idx ON idx.oid=i.indexrelid
      JOIN pg_am am ON am.oid=idx.relam
     WHERE i.indexrelid='idx_posts_title_trgm'::regclass
       AND i.indrelid='posts'::regclass
       AND i.indisvalid AND i.indisready
       AND am.amname='gin'
       AND pg_get_indexdef(i.indexrelid)=
           'CREATE INDEX idx_posts_title_trgm ON public.posts USING gin (title gin_trgm_ops)'
  ), 'posts.title 使用可用的 gin_trgm_ops GIN 索引');
  PERFORM _assert(EXISTS (
    SELECT 1
      FROM pg_index i
      JOIN pg_class idx ON idx.oid=i.indexrelid
      JOIN pg_am am ON am.oid=idx.relam
     WHERE i.indexrelid='idx_posts_content_trgm'::regclass
       AND i.indrelid='posts'::regclass
       AND i.indisvalid AND i.indisready
       AND am.amname='gin'
       AND pg_get_indexdef(i.indexrelid)=
           'CREATE INDEX idx_posts_content_trgm ON public.posts USING gin (content gin_trgm_ops)'
  ), 'posts.content 使用可用的 gin_trgm_ops GIN 索引');
  PERFORM _assert(EXISTS (
    SELECT 1
      FROM pg_index i
      JOIN pg_class idx ON idx.oid=i.indexrelid
      JOIN pg_am am ON am.oid=idx.relam
     WHERE i.indexrelid='idx_users_name_trgm'::regclass
       AND i.indrelid='users'::regclass
       AND i.indisvalid AND i.indisready
       AND am.amname='gin'
       AND pg_get_indexdef(i.indexrelid)=
           'CREATE INDEX idx_users_name_trgm ON public.users USING gin (name gin_trgm_ops)'
  ), 'users.name 使用可用的 gin_trgm_ops GIN 索引');

  PERFORM _assert(EXISTS (
    SELECT 1
      FROM pg_index i
     WHERE i.indexrelid='idx_posts_status_created_id'::regclass
       AND i.indrelid='posts'::regclass
       AND i.indisvalid AND i.indisready
       AND pg_get_indexdef(i.indexrelid)=
           'CREATE INDEX idx_posts_status_created_id ON public.posts USING btree (status, created_at DESC, id DESC) WHERE (deleted_at IS NULL)'
  ), '帖子 latest 游标使用 status + created_at + id 复合索引');
  PERFORM _assert(EXISTS (
    SELECT 1
      FROM pg_index i
     WHERE i.indexrelid='idx_notifications_recipient_created_id'::regclass
       AND i.indrelid='notifications'::regclass
       AND i.indisvalid AND i.indisready
       AND pg_get_indexdef(i.indexrelid)=
           'CREATE INDEX idx_notifications_recipient_created_id ON public.notifications USING btree (recipient_id, created_at DESC, id DESC)'
  ), '通知游标使用 recipient_id + created_at + id 复合索引');

  missing := '{}';
  FOREACH want IN ARRAY ARRAY[
    'trg_post_likes_sync_count','trg_favorites_sync_count','trg_comment_likes_sync_count','trg_comments_sync_counts',
    'trg_post_likes_keys_immutable','trg_favorites_keys_immutable','trg_comment_likes_keys_immutable','trg_comments_keys_immutable',
    'trg_post_images_retire_asset','trg_users_retire_avatar_asset','trg_users_activate_avatar_asset','trg_image_assets_forbid_delete'
    ,'trg_users_claim_name','trg_user_name_claims_forbid_update','trg_user_name_claims_forbid_delete'
    ,'trg_user_ban_records_immutable','trg_user_ban_records_forbid_delete'
    ,'trg_user_name_change_records_immutable','trg_user_name_change_records_forbid_delete'
    ,'trg_user_role_records_immutable','trg_user_role_records_forbid_delete'
    ,'trg_image_access_intents_validate','trg_image_access_intents_immutable'
  ] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE NOT tgisinternal AND tgname=want) THEN
      missing := missing || want;
    END IF;
  END LOOP;
  PERFORM _assert(cardinality(missing)=0, format('关键触发器齐全（缺失：%s）', missing));

  -- 反向断言：不得存在任何 updated_at 触发器（D20）
  PERFORM _assert(NOT EXISTS (SELECT 1 FROM pg_trigger WHERE NOT tgisinternal AND tgname LIKE '%touch_updated_at%'),
                  '不存在 updated_at 触发器');

  -- 业务表数与表级注释：数字由 SQL 现算，不手工维护
  PERFORM _assert((SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
                    WHERE n.nspname='public' AND c.relkind='r' AND obj_description(c.oid,'pg_class') IS NULL) = 0,
                  '所有业务表都有 COMMENT ON TABLE');
  PERFORM _assert((SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
                    WHERE n.nspname='public' AND c.relkind='r') = 33, '业务表共 33 张');
  PERFORM _assert((SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal) >= 20, '触发器数量符合预期下限');
END $$;

DROP FUNCTION _assert_rejects(text, text[], text);
DROP FUNCTION _assert(boolean, text);
\echo ''
\echo '########## 全部断言通过 ##########'
