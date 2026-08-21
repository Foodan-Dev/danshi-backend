-- 字典表种子数据
--
-- ⚠ 词表真源的勘误（务必读完再改这个文件）
--
-- 前端有**两套**食堂常量，一度被误认为只有一套：
--   1. src/services/config_service.ts 的 CANTEENS_FALLBACK —— 只有 4 项，
--      名称形如「邯郸校区南区食堂」。它服务于 Explore 筛选配置，
--      且四个 fetcher 全是本地 fallback，从不发网络请求。
--   2. src/constants/selects.ts 的 CANTEEN_OPTIONS —— 15 项，
--      名称形如「南区食堂」。**发帖页 post_screen.tsx 用的是这一套**，
--      也就是真正写进 posts.canteen 的值。
--
-- 两套名称不是一一对应（「邯郸校区南区食堂」vs「南区食堂」）。
-- 因此本文件以**发帖入口的 15 项为准**，否则新库的外键会拒绝绝大多数存量数据。
-- campus 由名称前缀推导，前端原表没有校区字段。
--
-- 这 15 项全部是餐厅级（食堂 / 餐厅）粒度，没有窗口级条目，
-- 所以 canteen_windows 种子为空，由管理端录入或经 dictionary_suggestions 审核后创建。
--
-- cuisines / flavors 仍取自 config_service.ts —— 发帖页的菜系用同一份，
-- 口味则是自由文本输入（见 go-rewrite-plan.md §4.19），改为受控词表是本次的产品变更。
--
-- 词表后续增删走新的迁移文件或管理端，不要改这个文件——它已经跑过的环境不会重放。

-- +goose Up

INSERT INTO canteens (code, name, campus, sort_order) VALUES
    ('canteen-danyuan',        '旦苑食堂',     '邯郸校区', 10),
    ('canteen-beiqu',          '北区食堂',     '邯郸校区', 20),
    ('canteen-nanqu',          '南区食堂',     '邯郸校区', 30),
    ('canteen-nanyuan',        '南苑食堂',     '邯郸校区', 40),
    ('canteen-jiaogong',       '教工餐厅',     '邯郸校区', 50),
    ('canteen-nanqu-minzu',    '南区民族餐厅', '邯郸校区', 60),
    ('canteen-jiangwan',       '江湾食堂',     '江湾校区', 110),
    ('canteen-jiangwan-guanghua','江湾光华餐厅','江湾校区', 120),
    ('canteen-jiangwan-bei',   '江湾北食堂',   '江湾校区', 130),
    ('canteen-jiangwan-minzu', '江湾民族餐厅', '江湾校区', 140),
    ('canteen-jiangwan-canche','江湾餐车',     '江湾校区', 150),
    ('canteen-fenglin',        '枫林食堂',     '枫林校区', 210),
    ('canteen-fenglin-minzu',  '枫林民族餐厅', '枫林校区', 220),
    ('canteen-huli',           '护理学院餐厅', '枫林校区', 230),
    ('canteen-zhangjiang',     '张江食堂',     '张江校区', 310);

INSERT INTO cuisines (name, sort_order) VALUES
    ('中式',   10),
    ('西式',   20),
    ('日式',   30),
    ('韩式',   40),
    ('东南亚', 50),
    ('其他',   99);

-- 注意：这份口味词表分类学上并不干净——「川菜/湘菜/粤菜」实为菜系，
-- 「微辣/中辣/特辣」是同一维度的档位。此处按前端现状原样种子化以保证
-- 存量数据可映射；重新划分口味体系属产品决策，需单独立项后再出迁移。
-- 另注：发帖页当前允许自由输入口味，存量数据里很可能有本表之外的值，
-- 迁移前必须导出 distinct 取值逐一裁决（见 data-migration-plan.md §5）。
INSERT INTO flavors (name, sort_order) VALUES
    ('清淡', 10),
    ('微辣', 20),
    ('中辣', 30),
    ('麻辣', 40),
    ('特辣', 50),
    ('香辣', 60),
    ('酸辣', 70),
    ('甜味', 80),
    ('咸鲜', 90),
    ('浓郁', 100),
    ('爽口', 110),
    ('家常', 120),
    ('川菜', 130),
    ('湘菜', 140),
    ('粤菜', 150),
    ('其他', 999);

-- +goose Down

DELETE FROM flavors  WHERE name IN (
    '清淡','微辣','中辣','麻辣','特辣','香辣','酸辣','甜味',
    '咸鲜','浓郁','爽口','家常','川菜','湘菜','粤菜','其他');
DELETE FROM cuisines WHERE name IN ('中式','西式','日式','韩式','东南亚','其他');
DELETE FROM canteens WHERE code IN (
    'canteen-danyuan','canteen-beiqu','canteen-nanqu','canteen-nanyuan',
    'canteen-jiaogong','canteen-nanqu-minzu','canteen-jiangwan',
    'canteen-jiangwan-guanghua','canteen-jiangwan-bei','canteen-jiangwan-minzu',
    'canteen-jiangwan-canche','canteen-fenglin','canteen-fenglin-minzu',
    'canteen-huli','canteen-zhangjiang');
