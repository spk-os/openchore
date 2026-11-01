-- 041: 朗读偏好服务端化——users.read_aloud。
-- 之前朗读开关只存浏览器 localStorage（openchore_tts_<id>），换设备/换
-- 浏览器即丢（电脑端看不到按钮、手机端默认值漂移）。改为随人存服务端：
-- NULL = 从未设置（前端按年龄回退默认：age<=7 开，其余关）；
-- 0/1 = 明确选择，所有设备一致。ALTER ADD COLUMN 即可（可空新列，无回填）。

ALTER TABLE users ADD COLUMN read_aloud INTEGER CHECK (read_aloud IN (0, 1) OR read_aloud IS NULL);
