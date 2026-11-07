// @vitest-environment jsdom
// 组件查询辅助（卡 TX-FE-CODER）：仓库约定选择器是 data-test（非 data-testid），
// 这里统一把 testing-library 的 testId 查询指到 data-test，测试用例保持
// getByTestId/queryByTestId 的可读写法。
//
// 注意：必须在 import 组件（从而 import @testing-library/react）的同一行序里
// 先于任何 screen 查询执行；vi.mock 工厂不触发本模块，故由各测试文件显式
// `import './testUtils'`（见用法）。
//
// ⚠️ 副作用导入必须在 '@testing-library/react' 之后（循环引用安全），
// 但要在查询发生前。Vitest 静态 import 全部完成后才执行副作用——因此
// 这里不用 import { configure } 而是 require 形式立即执行。

import { configure } from '@testing-library/react';

configure({ testIdAttribute: 'data-test' });

export {};
