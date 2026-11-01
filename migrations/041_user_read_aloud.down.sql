-- 041 down: 朗读偏好回退为仅 localStorage（去掉服务端列）。
-- 既有先例（009/016/020 down）对 users 加列的回滚一律 ALTER DROP COLUMN，
-- 不做整表重建——重建要处理十几个 REFERENCES users 的外键链，得不偿失。

ALTER TABLE users DROP COLUMN read_aloud;
