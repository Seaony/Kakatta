// 接口类型由 api/openapi.json 生成，请勿手改。

export interface paths {
    "/healthz": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 检查服务是否可用
         * @description 检查服务是否可用
         */
        get: operations["getHealth"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/setup": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取初始化状态
         * @description 读取初始化状态
         */
        get: operations["getSetupStatus"];
        put?: never;
        /**
         * 以一次性令牌建立管理员
         * @description 验证初始化令牌后原子建立管理员。令牌只在内存保留三十分钟，成功后不能再使用。
         */
        post: operations["setupAdmin"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/login": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 使用用户名和密码登录
         * @description 使用用户名和密码登录
         */
        post: operations["login"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/totp": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 完成登录的两步验证
         * @description 完成登录的两步验证
         */
        post: operations["verifyLoginTOTP"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/auth/logout": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 退出当前会话或令牌
         * @description 退出当前会话或令牌
         */
        post: operations["logout"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/me": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取当前身份与界面偏好
         * @description 读取当前身份与界面偏好
         */
        get: operations["getMe"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/settings": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取有效设置快照
         * @description 读取有效设置快照
         */
        get: operations["getSettings"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        /**
         * 原子保存设置补丁
         * @description 整个 values 补丁一起校验、一起保存。环境覆盖项只读；推荐范围之外只警告，不自动改值。时区保护记录由存储事务一起更新。
         */
        patch: operations["patchSettings"];
        trace?: never;
    };
    "/api/v1/settings/definitions": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取设置元数据
         * @description 读取设置元数据
         */
        get: operations["listSettingDefinitions"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/security/tokens": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 列出设备和令牌元数据
         * @description 列出设备和令牌元数据
         */
        get: operations["listSecurityTokens"];
        put?: never;
        /**
         * 创建接口令牌
         * @description 创建接口令牌
         */
        post: operations["createSecurityToken"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/security/tokens/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        /**
         * 吊销指定设备或令牌
         * @description 吊销指定设备或令牌
         */
        delete: operations["revokeSecurityToken"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/security/password": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 修改登录密码
         * @description 修改登录密码
         */
        post: operations["changePassword"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/security/totp/setup": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 开始绑定两步验证
         * @description 开始绑定两步验证
         */
        post: operations["beginTOTPSetup"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/security/totp/enable": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 验证并启用两步验证
         * @description 验证并启用两步验证
         */
        post: operations["enableTOTP"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/security/totp/disable": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 关闭两步验证
         * @description 关闭两步验证
         */
        post: operations["disableTOTP"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/events": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 订阅已认证的事件流
         * @description 订阅已认证的事件流
         */
        get: operations["streamEvents"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/openapi.json": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取完整接口描述
         * @description 读取完整接口描述
         */
        get: operations["getOpenAPI"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/tasks": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 列出任务
         * @description 列出任务
         */
        get: operations["listTasks"];
        put?: never;
        /**
         * 创建任务
         * @description 创建任务
         */
        post: operations["createTask"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/tasks/defaults": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取新任务草稿默认值
         * @description 草稿的 name/keyword 可以为空，保存时仍严格校验；不创建任务。
         */
        get: operations["getTaskDefaults"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/tasks/{task_id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取任务
         * @description 读取任务
         */
        get: operations["getTask"];
        put?: never;
        post?: never;
        /**
         * 删除任务
         * @description 先取消并等待相关执行者，再删除任务；收藏商品和历史运行名称保留。
         */
        delete: operations["deleteTask"];
        options?: never;
        head?: never;
        /**
         * 原子保存任务
         * @description 原子保存任务
         */
        patch: operations["updateTask"];
        trace?: never;
    };
    "/api/v1/tasks/{task_id}/runtime": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取任务实时状态
         * @description 读取任务实时状态
         */
        get: operations["getTaskRuntime"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/tasks/{task_id}/run": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 立即运行任务
         * @description 首次请求仅在资源忙时排队；已因资源占用入队的请求继续等待前次结束后自然产生的账号间隔，到期唤醒。排队期间仍重检其他全部适用门禁，阻断时移队记跳过。HTTP 请求结束不取消已接纳运行。
         */
        post: operations["runTask"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/tasks/{task_id}/cancel": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 取消当前运行或队列请求
         * @description 取消当前运行或队列请求
         */
        post: operations["cancelTask"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/tasks/{task_id}/no-good-hint/dismiss": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 忽略本次无合适结果提示
         * @description 忽略本次无合适结果提示
         */
        post: operations["dismissNoGoodHint"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/tasks/{task_id}/apply-exclusions": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 按用户确认隐藏新增排除词命中项
         * @description 再次核对词仍属当前任务及当前命中；不隐藏收藏或已拒绝项。
         */
        post: operations["applyTaskExclusions"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/tasks/{task_id}/market": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取当前行情及最新解读
         * @description 只用已保存资料计算，不发起抓取或模型请求；统计分位数保留浮点分。
         */
        get: operations["getTaskMarket"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/tasks/{task_id}/market/samples": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 列出行情实际比价样本
         * @description 价格、商品编号升序；配置为空列所选组去重样本，指定配置列实际参与该组计算的样本，包含放宽配置；响应保留样本真实 config_key。
         */
        get: operations["listTaskMarketSamples"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/risk-dictionary": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取固定风险字典
         * @description 读取固定风险字典
         */
        get: operations["listRiskDictionary"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/regions": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取随包地区层级
         * @description 读取随包地区层级
         */
        get: operations["listRegions"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/accounts": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 列出账号
         * @description 列出账号
         */
        get: operations["listAccounts"];
        put?: never;
        /**
         * 创建未登录账号
         * @description 创建未登录账号
         */
        post: operations["createAccount"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/accounts/{account_id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取账号状态和额度
         * @description 读取账号状态和额度
         */
        get: operations["getAccount"];
        put?: never;
        post?: never;
        /**
         * 关闭资源并删除账号
         * @description 关闭资源并删除账号
         */
        delete: operations["deleteAccount"];
        options?: never;
        head?: never;
        /**
         * 修改账号名称或代理绑定
         * @description 代理更改下次启动生效；不允许写风险状态、额度或间隔。
         */
        patch: operations["updateAccount"];
        trace?: never;
    };
    "/api/v1/accounts/{account_id}/logout": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 退出闲鱼账号并删除登录资料
         * @description 先关闭资源；不清额度、冷却、拒绝或已抽取的账号间隔。
         */
        post: operations["logoutAccount"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/accounts/{account_id}/login": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 打开手动登录会话
         * @description 全局名额忙时排队，实际启动后才开始会话期限；不自动处理验证码。
         */
        post: operations["openAccountLogin"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/accounts/{account_id}/verification": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 接手或重开验证会话
         * @description 仅使用服务端保存的验证目标；不接受客户端导航地址。
         */
        post: operations["openAccountVerification"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/accounts/{account_id}/verification/confirm": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 确认当前会话验证完成
         * @description 核对会话编号并在同一浏览器检查，不导航、不自动刷新。
         */
        post: operations["confirmAccountVerification"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/accounts/{account_id}/sessions/{session_id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取排队或活动会话
         * @description 读取不启动浏览器、不续期；旧会话编号不能指向后来创建的会话。
         */
        get: operations["getAccountSession"];
        put?: never;
        post?: never;
        /**
         * 关闭匹配的资源会话
         * @description 关闭匹配的资源会话
         */
        delete: operations["closeAccountSession"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/proxies": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 列出代理脱敏元数据
         * @description 列出代理脱敏元数据
         */
        get: operations["listProxies"];
        put?: never;
        /**
         * 创建代理
         * @description 创建代理
         */
        post: operations["createProxy"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/proxies/{proxy_id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        /**
         * 删除无引用代理
         * @description 账号或模型设置仍引用时拒绝删除。
         */
        delete: operations["deleteProxy"];
        options?: never;
        head?: never;
        /**
         * 更新代理
         * @description url 缺失保持原密文，null 非法；不回传 URL、认证信息或密文。
         */
        patch: operations["updateProxy"];
        trace?: never;
    };
    "/api/v1/proxies/{proxy_id}/test": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 测试代理端口连通性
         * @description 只用五秒连接配置 host:port，不访问网站；普通失败以 ok=false 和原因返回。
         */
        post: operations["testProxy"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/findings": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 列出发现
         * @description 列出发现
         */
        get: operations["listFindings"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/findings/{finding_id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取发现详情及上下条
         * @description 只读，不自动标已读；按同一过滤和排序上下文找邻居，不按编号加减，也不受分页游标限制。
         */
        get: operations["getFinding"];
        put?: never;
        post?: never;
        /**
         * 删除单条发现
         * @description 即使收藏也删除该发现，收藏商品仍在；seen 不变。
         */
        delete: operations["deleteFinding"];
        options?: never;
        head?: never;
        /**
         * 设置发现已读状态
         * @description 设置发现已读状态
         */
        patch: operations["updateFinding"];
        trace?: never;
    };
    "/api/v1/tasks/{task_id}/findings": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        /**
         * 清空任务全部非收藏发现
         * @description 不受分页上限；保留收藏商品的发现及 seen。
         */
        delete: operations["deleteTaskFindings"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/findings/reanalysis-preview": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 预览重新分析所需详情数量
         * @description 只读，返回当前缓存下的数量与 as_of；不修改状态或启动运行。
         */
        post: operations["previewReanalysis"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/findings/batch": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 批量删除、隐藏或请求重新分析
         * @description 服务端先稳定去重，要求去重后 1–200 个唯一正整数发现编号。delete/dismiss 返回 200 BatchResult；reanalyze 返回 202 ReanalysisResult 并全部进入正常运行队列，不在本请求偷偷调用模型。
         */
        post: operations["batchFindings"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/findings/{finding_id}/reanalyze": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 请求单件重新分析
         * @description 先按当前第一层复判；符合单件缓存例外时可即时重判，否则进入正常运行队列。保留用户规格/覆盖及首次推荐和降价基准。
         */
        post: operations["reanalyzeFinding"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/findings/{finding_id}/overrides": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * 替换该发现的用户风险覆盖
         * @description 替换该发现的用户风险覆盖
         */
        put: operations["setRiskOverrides"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/tasks/{task_id}/items/{item_id}/specs": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * 保存用户确认的任务级商品规格
         * @description source 固定为 user，重算配置、行情和关联评分，不访问浏览器或模型。
         */
        put: operations["setItemSpecs"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/findings/{finding_id}/extra-check": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 把清单外发现加入任务检查项
         * @description 核对当前事实仍含相同名称、证据、来源；过期返回冲突，避免按易变索引选错。
         */
        post: operations["addExtraCheck"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/listings": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 列出全局商品
         * @description 列出全局商品
         */
        get: operations["listListings"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/listings/{item_id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取独立商品及现存发现
         * @description 商品无需仍有关联任务；删除任务后的收藏仍可读。不发起抓取。
         */
        get: operations["getListing"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        /**
         * 修改全局收藏或不感兴趣标记
         * @description 仅修改 Kakatta 本地标记，对全部任务生效；缺失保持，null 非法。
         */
        patch: operations["updateListing"];
        trace?: never;
    };
    "/api/v1/listings/{item_id}/prices": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 分页读取完整商品价格点
         * @description observed_at/id 升序，不发起抓取。
         */
        get: operations["listListingPrices"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/listings/{item_id}/thumbnail": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取商品缩略图
         * @description 从本地缓存读取或通过任务账号代理下载已保存的缩略图。
         */
        get: operations["getListingThumbnail"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/blocked-sellers": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 列出全局屏蔽卖家
         * @description 列出全局屏蔽卖家
         */
        get: operations["listBlockedSellers"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/blocked-sellers/{seller_id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * 屏蔽卖家
         * @description 幂等本地操作，不访问闲鱼。
         */
        put: operations["blockSeller"];
        post?: never;
        /**
         * 取消屏蔽卖家
         * @description 不自动把旧商品变成新发现。
         */
        delete: operations["unblockSeller"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/overview": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取概览
         * @description 统一按当前时区、推荐定义和真实数据覆盖计算；比率不预乘 100。
         */
        get: operations["getOverview"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/runs": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 列出运行日志
         * @description 列出运行日志
         */
        get: operations["listRuns"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/runs/{run_id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取运行和首屏步骤
         * @description 读取运行和首屏步骤
         */
        get: operations["getRun"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/runs/{run_id}/steps": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取逐步日志
         * @description seq 升序；前 100 步之后仍保留全部失败步骤，不把总数硬截为 100。
         */
        get: operations["listRunSteps"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/runs/overview": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取过去二十四小时运行统计
         * @description 成功率不含 skipped，未结束运行不入完成统计；旧折叠 skip 无法恢复小时事实时 coverage.is_partial=true。
         */
        get: operations["getRunLogOverview"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/system/events": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 列出领域系统事件
         * @description 列出领域系统事件
         */
        get: operations["listSystemEvents"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/system/status": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取调度、资源、配置和额度状态
         * @description 读取调度、资源、配置和额度状态
         */
        get: operations["getSystemStatus"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/maintenance/clear-image-cache": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 清除本地图片缓存
         * @description 清除本地图片缓存
         */
        post: operations["clearImageCache"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/maintenance/clear-data": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 清除业务数据并保留保护记录
         * @description 先停止接纳、取消并等待执行者、关闭资源结算间隔，再事务清业务。保留认证/令牌、账号及登录资料、风险/间隔、代理/渠道/服务商/设置/提示词、额度、模型用量、时钟与初始化保护。
         */
        post: operations["clearBusinessData"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/remote/sessions/{session_id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 转到远程会话尾斜杠路径
         * @description 认证并确认会话存在后重定向；不启动资源、不计首次接手。
         */
        get: operations["redirectRemoteSession"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/remote/sessions/{session_id}/": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取远程查看页
         * @description 同源认证的远程查看页；静态读取不延长会话，不计首次接手。
         */
        get: operations["getRemoteViewer"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/remote/sessions/{session_id}/viewport": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取远程画面范围
         * @description 读取远程画面范围
         */
        get: operations["getRemoteViewport"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/remote/sessions/{session_id}/vnc": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 连接远程画面二进制流
         * @description WebSocket 升级，严格同源 Origin；二进制帧承载 RFB。只有成功 Accept 后首次触发 ViewerOpened；会话吊销或过期后停止已建立的流。
         */
        get: operations["connectRemoteVNC"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/remote/sessions/{session_id}/{asset_path}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取远程查看器静态资源
         * @description asset_path 可含多段路径，仅提供随包 viewer.js/viewer.css/text-entry.js/touch.js 和 novnc/** 等静态文件；不是导航地址。每次请求同样鉴权。
         */
        get: operations["getRemoteAsset"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/channels": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 列出全部通知渠道
         * @description 列出全部通知渠道
         */
        get: operations["listChannels"];
        put?: never;
        /**
         * 保存通知渠道
         * @description 保存通知渠道
         */
        post: operations["createChannel"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/channels/{channel_id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取通知渠道安全视图
         * @description 读取通知渠道安全视图
         */
        get: operations["getChannel"];
        put?: never;
        post?: never;
        /**
         * 删除渠道并返回受影响任务
         * @description 取消并等待本渠道工作，保留历史投递。删除最后一个显式系统通知目标返回 409，不把目标悄悄扩大为全部启用渠道。
         */
        delete: operations["deleteChannel"];
        options?: never;
        head?: never;
        /**
         * 更新通知渠道
         * @description 更新通知渠道
         */
        patch: operations["updateChannel"];
        trace?: never;
    };
    "/api/v1/channels/{channel_id}/test": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 用已保存渠道同步发送一次固定测试消息
         * @description 走同一渠道队列和间隔，失败仍返回 ok=false 与安全原因；不接受任意正文或商品。
         */
        post: operations["testChannel"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/channels/{channel_id}/deliveries": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取渠道最近二十条实际投递
         * @description 固定最近 20 条，无分页；包含安全错误原因和完整 item_count，不包含凭据。
         */
        get: operations["listChannelDeliveries"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/deliveries/{delivery_id}/items": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取一次投递关联的全部发现
         * @description 按 finding_id 完整排序，不受 20/200 分页限制；历史发现清理后仍保编号，标题可能为 null。系统和测试投递返回 []，不存在投递返回 404。
         */
        get: operations["listDeliveryItems"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/ai/providers": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 列出模型服务商
         * @description 列出模型服务商
         */
        get: operations["listAIProviders"];
        put?: never;
        /**
         * 创建模型服务商
         * @description 创建模型服务商
         */
        post: operations["createAIProvider"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/ai/providers/{provider_id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取模型服务商安全视图
         * @description 读取模型服务商安全视图
         */
        get: operations["getAIProvider"];
        /**
         * 整体保存模型服务商
         * @description 除 API Key 省略或 null 保留外，整体覆盖其他字段；不把脱敏占位当新密钥。
         */
        put: operations["updateAIProvider"];
        post?: never;
        /**
         * 删除模型服务商
         * @description 删除模型服务商
         */
        delete: operations["deleteAIProvider"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/ai/providers/{provider_id}/enabled": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * 修改模型服务商启用状态
         * @description 修改模型服务商启用状态
         */
        put: operations["setAIProviderEnabled"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/ai/providers/order": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * 保存完整服务商顺序
         * @description 保存完整服务商顺序
         */
        put: operations["reorderAIProviders"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/ai/providers/{provider_id}/test": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 依次试分析已保存服务商
         * @description 仅使用已保存配置，文本与视觉两步报告分别说明成功/失败/未执行；模型测试失败由 report 表达，基础设施失败用 HTTP 错误。实际请求仍共用额度和并发门禁。
         */
        post: operations["testAIProvider"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/prompts": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取四种用途的提示词
         * @description 读取四种用途的提示词
         */
        get: operations["listPrompts"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/prompts/{kind}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * 保存用途提示词
         * @description 保存用途提示词
         */
        put: operations["savePrompt"];
        post?: never;
        /**
         * 还原用途默认提示词
         * @description 还原用途默认提示词
         */
        delete: operations["restorePrompt"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/ai/profile": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 生成并合并需求清单草稿
         * @description 请求可取消；保留旧手改，建议词与预算须显式应用。金额响应为整数分；不自动保存任务或启动搜索。
         */
        post: operations["generateProfile"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/ai/usage": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取模型用量报告
         * @description 读取模型用量报告
         */
        get: operations["getAIUsage"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/ai/budget": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取保护后的每日模型额度
         * @description 读取保护后的每日模型额度
         */
        get: operations["getAIBudget"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/tasks/export": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 下载全部任务配置
         * @description 读取完整任务快照，包含所有任务，不受网页分页限制；UTF-8 JSON 文件，金额单位元，文件时间为 RFC3339。
         */
        get: operations["exportTasks"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/tasks/import": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 从任务文件导入配置
         * @description 仅上传 file。完整格式/外壳预检失败返回 4xx，不保存任务；预检完成后逐项保存，单项跳过不影响其他项。保存中断且已有报告时返回 200 和 complete=false 的真实计数，保留已成功任务，不自动整份重试。
         */
        post: operations["importTasks"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/tasks/{task_id}/findings/export": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 下载任务当前筛选下全部发现
         * @description 同一发现筛选和稳定排序的全量只读快照，不接受 task_id/limit/cursor 查询参数。UTF-8 BOM、RFC4180、CRLF 的 CSV，十三列按规格排列，金额写元单位数字，链接为桌面地址。
         */
        get: operations["exportTaskFindings"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/runs/export": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 下载当前筛选下全部运行和步骤
         * @description 使用当前设置语言，读取全部匹配运行及逐步日志，不接受 limit/cursor。UTF-8 文本，一条物理行一条记录，字段用制表符分隔；[from_at,to_at) 为 UTC 毫秒。
         */
        get: operations["exportRuns"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/system/update": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * 读取缓存的版本更新状态
         * @description 仅读取当前缓存和设置，不发起新检查、不下载更新、不执行命令。
         */
        get: operations["getUpdateStatus"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/backup/export": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 设置密码并下载加密备份
         * @description 用户提供本次备份密码。生成一致快照并分块加密，ZIP 包含原始文件清单和加密载荷；不包含闲鱼登录状态、会话/令牌、访问额度或图片缓存。
         */
        post: operations["exportBackup"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/backup/restore": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 上传并完整验证恢复候选
         * @description 仅接收 file/password。先完整校验、解密并验证候选，成功只返回安全预览和单次票据；本步骤不覆盖当前数据。文件按流式准备。
         */
        post: operations["prepareBackupRestore"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/api/v1/backup/confirm": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * 确认覆盖为已验证备份
         * @description 票据绑定原身份及当前运行代，确认前重新校验认证并单次消费。停止和排空旧资源后切换；成功 restored=true，所有旧会话与令牌失效，账号需重新扫码，使用备份中的密码重新登录。
         */
        post: operations["confirmBackupRestore"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export type webhooks = Record<string, never>;
export interface components {
    schemas: {
        /** @description 任意合法 JSON 值；设置键及值的业务校验由设置模块负责。 */
        JsonValue: null | boolean | number | string | components["schemas"]["JsonValue"][] | {
            [key: string]: components["schemas"]["JsonValue"];
        };
        LocalizedMessage: {
            code: string;
            /** @description 没有参数时仍返回空对象。 */
            args: {
                [key: string]: components["schemas"]["JsonValue"];
            };
        };
        FieldIssue: {
            key: string;
            reason: components["schemas"]["LocalizedMessage"];
        };
        ErrorEnvelope: {
            error: components["schemas"]["LocalizedMessage"];
            fields: components["schemas"]["FieldIssue"][];
            /** @description 服务端生成的请求编号，不照抄客户端输入。 */
            request_id: string;
        };
        HealthReady: {
            /** @constant */
            status: "ok";
        };
        HealthUnavailable: {
            /** @constant */
            status: "unavailable";
        };
        SetupStatus: {
            initialized: boolean;
            /**
             * Format: int64
             * @description UTC Unix 毫秒；尚无记录时为 null。
             */
            token_expires_at: number | null;
        };
        SetupRequest: {
            /** @description 容器日志中的一次性初始化令牌。 */
            token: string;
            username: string;
            /** @description 管理员密码。 */
            password: string;
        };
        LoginRequest: {
            username: string;
            /** @description 管理员密码。 */
            password: string;
        };
        TOTPLoginRequest: {
            /** @description 第一步登录发放的五分钟挑战。 */
            challenge: string;
            /** @description 验证器中的六位数字，保留开头的零。 */
            code: string;
        };
        Me: {
            username: string;
            /** @enum {string} */
            auth_kind: "web_session" | "api_token";
            /** Format: int64 */
            token_id: number;
            /** @enum {string} */
            language: "system" | "zh-CN" | "en";
            /** @enum {string} */
            appearance: "system" | "light" | "dark";
            totp_enabled: boolean;
        };
        AuthenticatedLogin: {
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            status: "authenticated";
            user: components["schemas"]["Me"];
        };
        TOTPRequiredLogin: {
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            status: "totp_required";
            /** @description 仅用于本次第二步验证的挑战，不是登录会话。 */
            challenge: string;
            /**
             * Format: int64
             * @description UTC Unix 毫秒。
             */
            expires_at: number;
        };
        LoginResult: components["schemas"]["AuthenticatedLogin"] | components["schemas"]["TOTPRequiredLogin"];
        SettingValues: {
            [key: string]: components["schemas"]["JsonValue"];
        };
        /** @enum {string} */
        SettingSource: "default" | "stored" | "environment";
        SettingsSnapshot: {
            values: components["schemas"]["SettingValues"];
            sources: {
                [key: string]: components["schemas"]["SettingSource"];
            };
            warnings: components["schemas"]["FieldIssue"][];
        };
        SettingsPatch: {
            values: components["schemas"]["SettingValues"];
        };
        SettingRecommendation: {
            min: number | null;
            max: number | null;
            text: components["schemas"]["LocalizedMessage"];
        };
        SettingDefinition: {
            key: string;
            group: string;
            /** @enum {string} */
            kind: "integer" | "boolean" | "string" | "range" | "quiet_hours" | "ids" | "strings" | "nullable_id";
            default: components["schemas"]["JsonValue"];
            unit: string;
            options: components["schemas"]["JsonValue"][];
            recommendation: components["schemas"]["SettingRecommendation"];
            label: components["schemas"]["LocalizedMessage"];
            why: components["schemas"]["LocalizedMessage"];
            risk: components["schemas"]["LocalizedMessage"];
            task_override: boolean;
        };
        TokenMetadata: {
            /** Format: int64 */
            id: number;
            /** @enum {string} */
            kind: "web_session" | "api_token";
            name: string;
            /**
             * Format: int64
             * @description UTC Unix 毫秒；尚无记录时为 null。
             */
            created_at: number | null;
            /**
             * Format: int64
             * @description UTC Unix 毫秒；尚无记录时为 null。
             */
            last_used_at: number | null;
            /**
             * Format: int64
             * @description UTC Unix 毫秒；尚无记录时为 null。
             */
            expires_at: number | null;
            /**
             * Format: int64
             * @description UTC Unix 毫秒；尚无记录时为 null。
             */
            revoked_at: number | null;
            current: boolean;
        };
        TokenList: {
            tokens: components["schemas"]["TokenMetadata"][];
        };
        CreateTokenRequest: {
            name: string;
        };
        IssuedToken: {
            /** Format: int64 */
            id: number;
            /** @description 仅在创建成功时返回一次；之后列表只返回元数据。 */
            token: string;
            /**
             * Format: int64
             * @description UTC Unix 毫秒。
             */
            created_at: number;
        };
        ChangePasswordRequest: {
            /** @description 当前密码。 */
            old_password: string;
            /** @description 新密码。 */
            new_password: string;
        };
        TOTPSetupRequest: {
            /** @description 当前密码。 */
            password: string;
        };
        TOTPSetup: {
            /** @description 本次绑定使用的临时令牌。 */
            setup_token: string;
            /** @description 仅在本次绑定响应中返回，验证成功后才保存。 */
            secret: string;
            /**
             * Format: uri
             * @description 验证器绑定 URI，仅在本次绑定响应中返回。
             */
            uri: string;
            /**
             * Format: int64
             * @description UTC Unix 毫秒。
             */
            expires_at: number;
        };
        TOTPEnableRequest: {
            /** @description 本次绑定返回的临时令牌。 */
            setup_token: string;
            /** @description 验证器中的六位数字，保留开头的零。 */
            code: string;
        };
        TOTPDisableRequest: {
            /** @description 当前密码。 */
            password: string;
            /** @description 验证器中的六位数字，保留开头的零。 */
            code: string;
        };
        /** @description 仓库 api/openapi.json 的完整内容。 */
        OpenAPIDocument: {
            [key: string]: components["schemas"]["JsonValue"];
        };
        AIOverview: {
            average_duration_ms: number | null;
            /** Format: int64 */
            count: number;
            /** @description 比率，0.1 表示 10%；没有足够事实时为 null。 */
            success_rate: number | null;
            /** Format: int64 */
            tokens: number;
        };
        AccountOverview: {
            /** Format: int64 */
            active: number;
            /** Format: int64 */
            total: number;
        };
        AccountQuotaView: {
            /** Format: int64 */
            account_id: number;
            name: string;
            quota: components["schemas"]["QuotaView"];
        };
        /** @enum {string} */
        AccountState: "signed_out" | "active" | "cooling" | "needs_verification" | "denied" | "expired";
        AccountView: {
            avatar_url: string | null;
            cooldown_manual_used: boolean;
            cooldown_until: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            created_at: number | null;
            /** Format: int64 */
            id: number;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            last_cooldown_end_at: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            last_run_ended_at: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            last_used_at: number | null;
            name: string;
            next_gap_seconds: number | null;
            nickname: string | null;
            proxy_id: number | null;
            quota: components["schemas"]["QuotaView"];
            /** @description 暂停尚未到期且账号已保存为退出登录状态时为 true，暂停到期后需重新扫码。 */
            relogin_after_pause: boolean;
            session: components["schemas"]["SessionView"] | null;
            state: components["schemas"]["AccountState"];
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            state_changed_at: number | null;
            state_reason: components["schemas"]["LocalizedMessage"] | null;
            /** Format: int64 */
            verify_level: number;
        };
        ActivityBucket: {
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            from_at: number;
            /** Format: int64 */
            new_count: number;
            /** Format: int64 */
            seen_count: number;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            to_at: number;
        };
        AnalysisAdjustment: {
            reason: string;
            /** Format: int64 */
            value: number;
        };
        /** @enum {string} */
        AnalysisAttitude: "confirm" | "reject" | "mind" | "ignore";
        AnalysisCheckFact: {
            evidence: string | null;
            key: string;
            /**
             * Format: int64
             * @description 条件含义的语义版本；事实和覆盖仅在版本相同时适用。
             */
            rev: number;
            source: string | null;
            status: components["schemas"]["AnalysisCheckStatus"];
        };
        /** @enum {string} */
        AnalysisCheckStatus: "ok" | "risk" | "suspect" | "unknown";
        AnalysisContactQuestion: {
            key: string;
            question: components["schemas"]["LocalizedMessage"];
        };
        AnalysisDailyPrice: {
            day: string;
            /** @description 分，保留统计插值精度，可带小数。 */
            median: number | null;
            /** Format: int64 */
            sample: number;
        };
        /** @enum {string} */
        AnalysisDecision: "pending" | "scored" | "matched" | "rejected" | "failed" | "skipped";
        AnalysisExtraFinding: {
            evidence: string;
            name: string;
            source: string;
        };
        AnalysisFacts: {
            adjust: components["schemas"]["AnalysisAdjustment"];
            bonuses: components["schemas"]["AnalysisRequirementFact"][];
            brand_new: boolean | null;
            checks: components["schemas"]["AnalysisCheckFact"][];
            extra_findings: components["schemas"]["AnalysisExtraFinding"][];
            musts: components["schemas"]["AnalysisRequirementFact"][];
            price_note: string | null;
            seller_signals: components["schemas"]["AnalysisSellerSignal"][];
            specs: components["schemas"]["AnalysisSpecValue"][];
            summary: string;
        };
        AnalysisFairPrice: {
            basis: components["schemas"]["LocalizedMessage"];
            /** Format: int64 */
            days: number;
            dropped_spec: string | null;
            item_ids: string[];
            /** @description 整数分；未知时为 null。 */
            price: number | null;
            /** Format: int64 */
            sample: number;
        };
        /** @enum {string} */
        AnalysisGrade: "steal" | "good" | "fair" | "pricey" | "avoid";
        /** @enum {string} */
        AnalysisKind: "sale" | "wanted" | "accessory" | "recycle" | "rental" | "repair" | "box" | "multi" | "broken" | "other";
        AnalysisMarketGroup: {
            brand_new: boolean;
            cheapest: components["schemas"]["AnalysisMarketSample"] | null;
            config_key: string;
            daily: components["schemas"]["AnalysisDailyPrice"][];
            fair: components["schemas"]["AnalysisFairPrice"];
            price_band: components["schemas"]["AnalysisPriceBand"] | null;
            samples: components["schemas"]["AnalysisMarketSample"][];
            selected: boolean;
            specs: components["schemas"]["AnalysisSpecValue"][];
            trend: components["schemas"]["AnalysisPriceTrend"] | null;
        };
        AnalysisMarketResult: {
            groups: components["schemas"]["AnalysisMarketGroup"][];
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            snapshot_at: number | null;
            task_trend: components["schemas"]["AnalysisDailyPrice"][];
            warnings: components["schemas"]["LocalizedMessage"][];
        };
        AnalysisMarketSample: {
            day: string;
            item_id: string;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            last_seen_at: number;
            /**
             * Format: int64
             * @description 整数分。
             */
            price: number;
            seller_id: string;
        };
        /** @enum {string} */
        AnalysisMode: "ai" | "keyword";
        AnalysisPriceBand: {
            /**
             * Format: double
             * @description 分，保留统计插值精度，可带小数。
             */
            p25: number;
            /**
             * Format: double
             * @description 分，保留统计插值精度，可带小数。
             */
            p75: number;
        };
        AnalysisPriceTrend: {
            /**
             * Format: double
             * @description 分，保留统计插值精度，可带小数。
             */
            change: number;
            first_day: string;
            /**
             * Format: double
             * @description 分，保留统计插值精度，可带小数。
             */
            first_median: number;
            last_day: string;
            /**
             * Format: double
             * @description 分，保留统计插值精度，可带小数。
             */
            last_median: number;
            /** @description 变化比率，0.1 表示 10%；无法计算时为 null。 */
            percent: number | null;
        };
        /** @description 关键词模式返回空对象；有清单时返回完整成员。 */
        AnalysisProfile: Record<string, never> | components["schemas"]["AnalysisProfileFields"];
        AnalysisProfileRisk: {
            attitude: components["schemas"]["AnalysisAttitude"];
            edited: boolean;
            hint?: string;
            id: string;
            name?: string;
            question?: string;
            /**
             * Format: int64
             * @description 条件含义的语义版本；事实和覆盖仅在版本相同时适用。
             */
            rev: number;
            source: components["schemas"]["AnalysisProfileSource"];
        };
        /** @enum {string} */
        AnalysisProfileSource: "user" | "ai" | "system";
        AnalysisProfileSpec: {
            edited: boolean;
            name: string;
            options: string[];
            source: components["schemas"]["AnalysisProfileSource"];
            values: string[];
        };
        AnalysisReasonCount: {
            /** Format: int64 */
            count: number;
            reason: components["schemas"]["LocalizedMessage"];
        };
        /** @enum {string} */
        AnalysisRejectKind: "filter" | "excluded" | "keyword_miss" | "junk" | "unmatched";
        AnalysisRequirement: {
            edited: boolean;
            id: string;
            /**
             * Format: int64
             * @description 条件含义的语义版本；事实和覆盖仅在版本相同时适用。
             */
            rev: number;
            source: components["schemas"]["AnalysisProfileSource"];
            text: string;
        };
        AnalysisRequirementFact: {
            evidence: string | null;
            id: string;
            /**
             * Format: int64
             * @description 条件含义的语义版本；事实和覆盖仅在版本相同时适用。
             */
            rev: number;
            source: string | null;
            status: components["schemas"]["AnalysisRequirementStatus"];
        };
        /** @enum {string} */
        AnalysisRequirementStatus: "met" | "unmet" | "unknown";
        AnalysisRiskDefinition: {
            category: string;
            default_attitude: components["schemas"]["AnalysisAttitude"];
            hint: string;
            id: string;
            name: string;
            question: string;
        };
        AnalysisRiskOverride: {
            /**
             * Format: int64
             * @description 条件含义的语义版本；事实和覆盖仅在版本相同时适用。
             */
            rev: number;
            status: components["schemas"]["AnalysisCheckStatus"];
        };
        AnalysisScoreBase: {
            basis: components["schemas"]["LocalizedMessage"];
            /** Format: double */
            d: number;
            /**
             * Format: int64
             * @description 整数分。
             */
            fair_price: number;
            /** Format: int64 */
            sample: number;
            /** Format: double */
            score: number;
        };
        AnalysisScoreBreakdown: {
            ai: components["schemas"]["AnalysisAdjustment"];
            base: components["schemas"]["AnalysisScoreBase"] | null;
            caps: components["schemas"]["AnalysisScoreCap"][];
            gates: components["schemas"]["LocalizedMessage"][];
            items: components["schemas"]["AnalysisScoreItem"][];
        };
        AnalysisScoreCap: {
            /** Format: int64 */
            limit: number;
            reason: components["schemas"]["LocalizedMessage"];
        };
        AnalysisScoreItem: {
            id: string;
            name: string;
            reason: components["schemas"]["LocalizedMessage"];
            source: string;
            /** Format: int64 */
            value: number;
        };
        AnalysisSellerSignal: {
            evidence: string;
            key: string;
        };
        /** @enum {string} */
        AnalysisSellerType: "personal" | "dealer_suspect" | "unknown";
        /** @enum {string} */
        AnalysisSpecSource: "title" | "detail" | "user";
        AnalysisSpecValue: {
            name: string;
            value: string | null;
        };
        /** @description risks 是检查项编号到 {status,rev} 的对象；空对象清除覆盖。版本必须匹配当前清单，覆盖不改模型事实。 */
        AnalysisUserOverrides: {
            risks: {
                [key: string]: components["schemas"]["AnalysisRiskOverride"];
            };
        };
        BatchItem: {
            /** Format: int64 */
            id: number;
            reason: components["schemas"]["LocalizedMessage"] | null;
            status: components["schemas"]["BatchStatus"];
        };
        BatchResult: {
            /** Format: int64 */
            applied: number;
            items: components["schemas"]["BatchItem"][];
            /** Format: int64 */
            requested: number;
            /** Format: int64 */
            skipped: number;
        };
        /** @enum {string} */
        BatchStatus: "applied" | "skipped";
        BlockedSeller: {
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            created_at: number | null;
            nick: string | null;
            seller_id: string;
        };
        BrowserStatus: {
            /** Format: int64 */
            active: number;
            /** Format: int64 */
            limit: number;
            sessions: components["schemas"]["SessionView"][];
        };
        CleanupResult: {
            deleted: {
                [key: string]: number;
            };
            /** Format: int64 */
            images_deleted: number;
            warnings: components["schemas"]["LocalizedMessage"][];
        };
        ConfigurationCheck: {
            key: string;
            ok: boolean;
            reason: components["schemas"]["LocalizedMessage"] | null;
        };
        /** @description from_at=null 表示没有可用事实；保留范围不足或旧折叠跳过记录无法重建小时分布时 is_partial=true。 */
        CoverageView: {
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            from_at: number | null;
            is_partial: boolean;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            to_at: number;
        };
        DeliverySummary: {
            at: number | null;
            channel_id: number | null;
            channel_name: string | null;
            error: components["schemas"]["LocalizedMessage"] | null;
            /** Format: int64 */
            id: number;
            item_count: number | null;
            kind: string | null;
            ok: boolean | null;
            task_id: number | null;
        };
        ExceptionOverview: {
            /** Format: int64 */
            network_failure_count: number;
            /** Format: int64 */
            risk_failure_count: number;
            unresolved_events: components["schemas"]["SystemEvent"][];
        };
        /** @enum {string} */
        FailureKind: "risk_verify" | "risk_deny" | "risk_env" | "login" | "network" | "page" | "timeout" | "quota" | "config" | "cancelled" | "restart";
        Finding: {
            analysis: components["schemas"]["AnalysisFacts"] | null;
            analysis_model: string | null;
            analysis_provider_id: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            analyzed_at: number | null;
            /** Format: int64 */
            attempts: number;
            decided_layer: number | null;
            decision: components["schemas"]["AnalysisDecision"];
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            drop_notified_at: number | null;
            fair_basis: components["schemas"]["LocalizedMessage"] | null;
            /** @description 整数分；未知时为 null。 */
            fair_price: number | null;
            fair_sample: number | null;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            found_at: number;
            grade: components["schemas"]["AnalysisGrade"] | null;
            /** Format: int64 */
            id: number;
            is_baseline: boolean;
            is_read: boolean;
            item_id: string;
            /** @description 整数分；未知时为 null。 */
            last_drop_notified_price: number | null;
            matched_keywords: string[];
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            notified_at: number | null;
            notify_kind: string | null;
            notify_skip_reason: components["schemas"]["LocalizedMessage"] | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            reanalyze_requested_at: number | null;
            reason: components["schemas"]["LocalizedMessage"] | null;
            /** @description 整数分；未知时为 null。 */
            recommended_price: number | null;
            reject_kind: components["schemas"]["AnalysisRejectKind"] | null;
            retryable: boolean;
            score: number | null;
            score_breakdown: components["schemas"]["AnalysisScoreBreakdown"] | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            scored_at: number | null;
            /** @description 整数分；未知时为 null。 */
            scored_price: number | null;
            /** Format: int64 */
            search_generation: number;
            seller_type: components["schemas"]["AnalysisSellerType"] | null;
            stage: components["schemas"]["FindingStage"];
            summary: string | null;
            /** Format: int64 */
            task_id: number;
            title_screen_reason: string | null;
            user_overrides: components["schemas"]["AnalysisUserOverrides"];
            /** @description 分析时同一事务保存的卖家派生判断，缓存清理后仍保留；旧记录没有事实时为 null，不能以当前卖家缓存冒充当时快照。 */
            seller_assessment: components["schemas"]["AnalysisSellerResult"] | null;
            /** @description 仅保存程序 ParseFacts 实际产生的解析警告，不从模型原始字段接收元数据；没有警告或旧记录无元数据时为 []。 */
            analysis_warnings: components["schemas"]["LocalizedMessage"][];
        };
        /** @enum {string} */
        FindingStage: "title" | "detail" | "done";
        FindingNotificationStatus: {
            /** @enum {string} */
            state: "retrying" | "cancelled" | "abandoned";
            reason: components["schemas"]["LocalizedMessage"] | null;
        };
        /** @description pending/failed 重分析不以旧分数冒充当前结果：score、grade、score_breakdown 为 null。previous_id/next_id 依据相同查询上下文定位，未计算或不存在时为 null。 */
        FindingView: {
            analysis: components["schemas"]["AnalysisFacts"] | null;
            analysis_model: string | null;
            analysis_provider_id: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            analyzed_at: number | null;
            /** Format: int64 */
            attempts: number;
            decided_layer: number | null;
            decision: components["schemas"]["AnalysisDecision"];
            deliveries: components["schemas"]["DeliverySummary"][];
            /** @description 首次推荐或新商品提醒的重试和终止状态。 */
            notification_status?: components["schemas"]["FindingNotificationStatus"] | null;
            /** @description 每个渠道最近的降价投递，包含发在合并提醒里的降价。 */
            price_drop_deliveries?: components["schemas"]["DeliverySummary"][];
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            drop_notified_at: number | null;
            fair_basis: components["schemas"]["LocalizedMessage"] | null;
            /** @description 整数分；未知时为 null。 */
            fair_price: number | null;
            fair_sample: number | null;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            found_at: number;
            grade: components["schemas"]["AnalysisGrade"] | null;
            /** Format: int64 */
            id: number;
            is_baseline: boolean;
            is_read: boolean;
            item_id: string;
            item_specs: components["schemas"]["TaskItemSpecs"] | null;
            /** @description 整数分；未知时为 null。 */
            last_drop_notified_price: number | null;
            listing: components["schemas"]["Listing"];
            matched_keywords: string[];
            next_id: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            notified_at: number | null;
            notify_kind: string | null;
            notify_skip_reason: components["schemas"]["LocalizedMessage"] | null;
            previous_id: number | null;
            questions: components["schemas"]["AnalysisContactQuestion"][];
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            reanalyze_requested_at: number | null;
            reason: components["schemas"]["LocalizedMessage"] | null;
            /** @description 整数分；未知时为 null。 */
            recommended_price: number | null;
            reject_kind: components["schemas"]["AnalysisRejectKind"] | null;
            retryable: boolean;
            score: number | null;
            score_breakdown: components["schemas"]["AnalysisScoreBreakdown"] | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            scored_at: number | null;
            /** @description 整数分；未知时为 null。 */
            scored_price: number | null;
            /** Format: int64 */
            search_generation: number;
            seller: components["schemas"]["SellerProfile"] | null;
            seller_type: components["schemas"]["AnalysisSellerType"] | null;
            stage: components["schemas"]["FindingStage"];
            summary: string | null;
            task: components["schemas"]["TaskSummary"];
            /** Format: int64 */
            task_id: number;
            title_screen_reason: string | null;
            user_overrides: components["schemas"]["AnalysisUserOverrides"];
            checks: components["schemas"]["AnalysisEffectiveCheck"][];
            market_comparison: components["schemas"]["FindingMarketComparison"] | null;
            /** @description 分析时同一事务保存的卖家派生判断，缓存清理后仍保留；旧记录没有事实时为 null，不能以当前卖家缓存冒充当时快照。 */
            seller_assessment: components["schemas"]["AnalysisSellerResult"] | null;
            /** @description 仅保存程序 ParseFacts 实际产生的解析警告，不从模型原始字段接收元数据；没有警告或旧记录无元数据时为 []。 */
            analysis_warnings: components["schemas"]["LocalizedMessage"][];
        };
        GradeCount: {
            /** Format: int64 */
            count: number;
            kind: string;
        };
        ItemSpecsUpdateResult: {
            item_specs: components["schemas"]["TaskItemSpecs"];
            /** Format: int64 */
            rescored_count: number;
        };
        Listing: {
            area: string | null;
            browse_count: number | null;
            description: string | null;
            desktop_url: string;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            detail_fetched_at: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            favorited_at: number | null;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            first_seen_at: number;
            image_urls: string[];
            is_dismissed: boolean;
            is_favorite: boolean;
            item_id: string;
            /** @description 整数分；未知时为 null。 */
            last_drop_amount: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            last_drop_at: number | null;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            last_seen_at: number;
            original_price_text: string | null;
            /** @description 整数分；未知时为 null。 */
            price: number | null;
            price_text: string | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            published_at: number | null;
            sale_status: string | null;
            seller_id: string | null;
            seller_nick: string | null;
            seller_registration_days: number | null;
            tags: string[];
            thumbnail_url: string | null;
            title: string;
            want_count: number | null;
            zhima_level: string | null;
        };
        ListingView: {
            area: string | null;
            browse_count: number | null;
            description: string | null;
            desktop_url: string;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            detail_fetched_at: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            favorited_at: number | null;
            findings: components["schemas"]["FindingView"][];
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            first_seen_at: number;
            image_urls: string[];
            is_dismissed: boolean;
            is_favorite: boolean;
            item_id: string;
            /** @description 整数分；未知时为 null。 */
            last_drop_amount: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            last_drop_at: number | null;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            last_seen_at: number;
            original_price_text: string | null;
            /** @description 整数分；未知时为 null。 */
            price: number | null;
            price_text: string | null;
            prices: components["schemas"]["PricePoint"][];
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            published_at: number | null;
            sale_status: string | null;
            seller: components["schemas"]["SellerProfile"] | null;
            seller_id: string | null;
            seller_nick: string | null;
            seller_registration_days: number | null;
            tags: string[];
            thumbnail_url: string | null;
            title: string;
            want_count: number | null;
            zhima_level: string | null;
        };
        MarketInsight: {
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            generated_at: number | null;
            /** Format: int64 */
            id: number;
            model: string | null;
            provider_id: number | null;
            stats: components["schemas"]["AnalysisMarketResult"] | null;
            task_id: number | null;
            text: string | null;
        };
        MarketSampleView: {
            brand_new: boolean;
            config_key: string;
            desktop_url: string;
            item_id: string;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            observed_at: number;
            /**
             * Format: int64
             * @description 整数分。
             */
            price: number;
            seller_id: string | null;
            title: string;
        };
        MarketView: {
            insight: components["schemas"]["MarketInsight"] | null;
            market: components["schemas"]["AnalysisMarketResult"];
            /** @description 近 14 个自然日的个人卖家占比，无可分类卖家时为 null。 */
            personal_seller_share: number | null;
            /** Format: int64 */
            today_new_count: number;
            /** Format: int64 */
            today_recommended_count: number;
        };
        MetricComparison: {
            /** Format: int64 */
            delta: number;
            /** Format: int64 */
            previous_count: number;
            /** @description 比率，0.1 表示 10%；没有足够事实时为 null。 */
            ratio: number | null;
        };
        MutationResult: {
            /** Format: int64 */
            applied: number;
        };
        NoGoodHint: {
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            latest_analyzed_at: number;
            reasons: components["schemas"]["AnalysisReasonCount"][];
            /** Format: int64 */
            sample_count: number;
        };
        NotificationOverview: {
            /** Format: int64 */
            count: number;
            /** @description 比率，0.1 表示 10%；没有足够事实时为 null。 */
            success_rate: number | null;
        };
        OverviewCards: {
            /** Format: int64 */
            new_count: number;
            /** Format: int64 */
            price_drop_count: number;
            /** Format: int64 */
            recommended_ai_count: number;
            /** Format: int64 */
            recommended_count: number;
            /** Format: int64 */
            recommended_keyword_count: number;
            /** Format: int64 */
            scanned_count: number;
        };
        OverviewComparison: {
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            from_at: number;
            new: components["schemas"]["MetricComparison"];
            price_drop: components["schemas"]["MetricComparison"];
            recommended: components["schemas"]["MetricComparison"];
            scanned: components["schemas"]["MetricComparison"];
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            to_at: number;
        };
        OverviewView: {
            accounts: components["schemas"]["AccountOverview"];
            activity: components["schemas"]["ActivityBucket"][];
            ai: components["schemas"]["AIOverview"];
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            as_of: number;
            cards: components["schemas"]["OverviewCards"];
            comparison: components["schemas"]["OverviewComparison"] | null;
            coverage: components["schemas"]["CoverageView"];
            exceptions: components["schemas"]["ExceptionOverview"];
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            from_at: number;
            grade_distribution: components["schemas"]["GradeCount"][];
            notifications: components["schemas"]["NotificationOverview"];
            period: string;
            /** @description 全部当前任务的完整快照，无分页；按各任务 state 准确统计任务状态数量。 */
            tasks: components["schemas"]["TaskOverview"][];
            timezone: string;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            to_at: number;
        };
        PageAccountView: {
            items: components["schemas"]["AccountView"][];
            next_cursor: string | null;
            /** Format: int64 */
            total: number;
        };
        PageBlockedSeller: {
            items: components["schemas"]["BlockedSeller"][];
            next_cursor: string | null;
            /** Format: int64 */
            total: number;
        };
        PageFindingView: {
            items: components["schemas"]["FindingView"][];
            next_cursor: string | null;
            /** Format: int64 */
            total: number;
        };
        PageListingView: {
            items: components["schemas"]["ListingView"][];
            next_cursor: string | null;
            /** Format: int64 */
            total: number;
        };
        PageMarketSampleView: {
            items: components["schemas"]["MarketSampleView"][];
            next_cursor: string | null;
            /** Format: int64 */
            total: number;
        };
        PagePricePoint: {
            items: components["schemas"]["PricePoint"][];
            next_cursor: string | null;
            /** Format: int64 */
            total: number;
        };
        PageProxyView: {
            items: components["schemas"]["ProxyView"][];
            next_cursor: string | null;
            /** Format: int64 */
            total: number;
        };
        PageRunStep: {
            items: components["schemas"]["RunStep"][];
            next_cursor: string | null;
            /** Format: int64 */
            total: number;
        };
        PageRunView: {
            items: components["schemas"]["RunView"][];
            next_cursor: string | null;
            /** Format: int64 */
            total: number;
        };
        PageSystemEvent: {
            items: components["schemas"]["SystemEvent"][];
            next_cursor: string | null;
            /** Format: int64 */
            total: number;
        };
        PageTaskView: {
            items: components["schemas"]["TaskView"][];
            next_cursor: string | null;
            /** Format: int64 */
            total: number;
        };
        PricePoint: {
            /** Format: int64 */
            id: number;
            item_id: string;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            observed_at: number;
            /**
             * Format: int64
             * @description 整数分。
             */
            price: number;
        };
        ProxyTestResult: {
            ok: boolean;
            reason: components["schemas"]["LocalizedMessage"] | null;
        };
        ProxyView: {
            configured: boolean;
            /** Format: int64 */
            id: number;
            name: string;
        };
        QuotaView: {
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            as_of: number;
            /** Format: int64 */
            daily_detail_limit: number;
            /** Format: int64 */
            daily_detail_used: number;
            /** Format: int64 */
            daily_search_limit: number;
            /** Format: int64 */
            daily_search_used: number;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            day_start_at: number;
            /** Format: int64 */
            hourly_limit: number;
            /** Format: int64 */
            hourly_used: number;
        };
        ReanalysisItem: {
            /** Format: int64 */
            finding_id: number;
            needs_detail: boolean;
            reason: components["schemas"]["LocalizedMessage"] | null;
            status: components["schemas"]["ReanalysisStatus"];
        };
        ReanalysisPreview: {
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            as_of: number;
            /** Format: int64 */
            cached_detail_count: number;
            /** Format: int64 */
            needs_detail_count: number;
            /** Format: int64 */
            requested_count: number;
        };
        ReanalysisResult: {
            items: components["schemas"]["ReanalysisItem"][];
            /** Format: int64 */
            needs_detail_count: number;
        };
        /** @enum {string} */
        ReanalysisStatus: "rejected" | "queued" | "cached";
        RegionNode: {
            id: string;
            level: string;
            name: string;
            parent_id: string | null;
        };
        RunDetail: {
            run: components["schemas"]["RunView"];
            steps: components["schemas"]["PageRunStep"];
        };
        RunHourBucket: {
            /** Format: int64 */
            failed_count: number;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            from_at: number;
            /** Format: int64 */
            skipped_count: number;
            /** Format: int64 */
            success_count: number;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            to_at: number;
        };
        RunLogOverview: {
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            as_of: number;
            average_duration_ms: number | null;
            buckets: components["schemas"]["RunHourBucket"][];
            coverage: components["schemas"]["CoverageView"];
            /** Format: int64 */
            failed_count: number;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            from_at: number;
            /** Format: int64 */
            skipped_count: number;
            /** Format: int64 */
            success_count: number;
            /** @description 比率，0.1 表示 10%；没有足够事实时为 null。 */
            success_rate: number | null;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            to_at: number;
            /** Format: int64 */
            total_count: number;
        };
        /** @enum {string} */
        RunOutcome: "success" | "failed" | "skipped";
        RunRecord: {
            account_id: number | null;
            account_name: string | null;
            analyzed_count: number | null;
            detail_count: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            ended_at: number | null;
            failure_kind: components["schemas"]["FailureKind"] | null;
            /** Format: int64 */
            id: number;
            layer1_rejected: number | null;
            layer2_count: number | null;
            layer2_rejected: number | null;
            message: components["schemas"]["LocalizedMessage"] | null;
            new_count: number | null;
            notified_count: number | null;
            outcome: components["schemas"]["RunOutcome"] | null;
            page_count: number | null;
            proxy_name: string | null;
            recommended_count: number | null;
            /** Format: int64 */
            repeat_count: number;
            scanned_count: number | null;
            seller_page_count: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            started_at: number | null;
            task_id: number | null;
            task_name: string;
            trigger: components["schemas"]["RunTrigger"];
            warnings: components["schemas"]["LocalizedMessage"][];
        };
        RunStep: {
            at: number | null;
            detail: components["schemas"]["LocalizedMessage"] | null;
            message: components["schemas"]["LocalizedMessage"] | null;
            /** Format: int64 */
            run_id: number;
            /** Format: int64 */
            seq: number;
            status: components["schemas"]["StepStatus"] | null;
        };
        RunSubmission: {
            run_id: number | null;
            status: components["schemas"]["SubmissionStatus"];
            /** Format: int64 */
            task_id: number;
        };
        /** @enum {string} */
        RunTrigger: "auto" | "manual";
        /** @description 未结束时 ended_at、outcome、failure_kind、message 均为 null；最终统计只计已结束记录。 */
        RunView: components["schemas"]["RunRecord"];
        RuntimeBlocked: {
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            next_allowed_at: number | null;
            reason: components["schemas"]["LocalizedMessage"];
        };
        SchedulerStatus: {
            accepting: boolean;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            next_wake_at: number | null;
            /** Format: int64 */
            queued_count: number;
            running: boolean;
            /** Format: int64 */
            running_count: number;
        };
        SellerProfile: {
            avatar_url: string | null;
            buyer_credit: string | null;
            buyer_credit_level: string | null;
            /** @description 比率，0.1 表示 10%；没有足够事实时为 null。 */
            buyer_good_rate: number | null;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            fetched_at: number;
            introduction: string | null;
            item_count: number | null;
            nick: string | null;
            on_sale_count: number | null;
            rate_count: number | null;
            /** @description 未取得时为 null，已取得空列表时为 []。 */
            recent_items: components["schemas"]["SellerRecentItem"][] | null;
            /** @description 未取得时为 null，已取得空列表时为 []。 */
            recent_rates: components["schemas"]["SellerRecentRate"][] | null;
            seller_credit: string | null;
            seller_credit_level: string | null;
            /** @description 比率，0.1 表示 10%；没有足够事实时为 null。 */
            seller_good_rate: number | null;
            seller_id: string;
            sold_count: number | null;
            /** @description 已取得评价时的真实计数；未知为 null，已取得的空列表为 0。 */
            seller_good_count: number | null;
            /** @description 已取得评价时的真实计数；未知为 null，已取得的空列表为 0。 */
            seller_rate_count: number | null;
            /** @description 已取得评价时的真实计数；未知为 null，已取得的空列表为 0。 */
            buyer_good_count: number | null;
            /** @description 已取得评价时的真实计数；未知为 null，已取得的空列表为 0。 */
            buyer_rate_count: number | null;
        };
        SellerRecentItem: {
            /** @description 整数分；未知时为 null。 */
            price: number | null;
            sale_status: string | null;
            thumbnail_url: string | null;
            title: string;
        };
        SellerRecentRate: {
            kind: string | null;
            role: string | null;
            text: string | null;
        };
        /** @enum {string} */
        SessionKind: "run" | "login" | "verification";
        /** @enum {string} */
        SessionPhase: "queued" | "opening" | "running" | "waiting_login" | "verify_hold" | "verifying" | "closing";
        /** @description 只表示排队或活动资源；关闭后账号 session 为 null。排队时 started_at/expires_at/remote_path 为 null；只有真实远程连接首次接受时计首次接手。 */
        SessionView: {
            /** Format: int64 */
            account_id: number;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            expires_at: number | null;
            id: string;
            kind: components["schemas"]["SessionKind"];
            phase: components["schemas"]["SessionPhase"];
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            queued_at: number | null;
            /** @description 重开验证网址后当前画面没有验证框，用户可以在远程画面里手动刷新。 */
            refresh_verification?: boolean;
            remote_path: string | null;
            run_id: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            started_at: number | null;
            task_id: number | null;
            viewport: components["schemas"]["Viewport"] | null;
        };
        /** @enum {string} */
        StepStatus: "ok" | "warn" | "fail";
        /** @enum {string} */
        SubmissionStatus: "queued" | "started";
        SystemEvent: {
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            created_at: number | null;
            /** Format: int64 */
            id: number;
            kind: components["schemas"]["SystemEventKind"] | null;
            message: components["schemas"]["LocalizedMessage"] | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            notified_at: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            resolved_at: number | null;
            subject_id: number | null;
        };
        /** @enum {string} */
        SystemEventKind: "account_verify" | "account_login" | "account_deny" | "ai_error" | "ai_budget" | "task_failure_pause" | "xianyu_layout" | "login_failures";
        SystemResources: {
            disk_free_bytes: number | null;
            disk_total_bytes: number | null;
            memory_total_bytes: number | null;
            memory_used_bytes: number | null;
        };
        SystemStatus: {
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            as_of: number;
            browsers: components["schemas"]["BrowserStatus"];
            configuration: components["schemas"]["ConfigurationCheck"][];
            quotas: components["schemas"]["AccountQuotaView"][];
            resources: components["schemas"]["SystemResources"];
            scheduler: components["schemas"]["SchedulerStatus"];
        };
        Task: {
            account_id: number | null;
            account_pinned: boolean;
            analyze_on_first_run: boolean;
            baseline_pending: boolean;
            /** @description 整数分；未知时为 null。 */
            budget_max: number | null;
            /** @description 整数分；未知时为 null。 */
            budget_min: number | null;
            channel_ids: number[];
            /** Format: int64 */
            consecutive_failures: number;
            crawl_overrides: components["schemas"]["SettingValues"];
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            created_at: number | null;
            enabled: boolean;
            exclude_keywords: string[];
            failure_paused_until: number | null;
            free_shipping_only: boolean;
            /** Format: int64 */
            id: number;
            /** Format: int64 */
            interval_minutes: number;
            judge_mode: components["schemas"]["AnalysisMode"];
            keyword: string;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            last_run_at: number | null;
            last_run_outcome: string | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            last_success_at: number | null;
            match_keywords: string[];
            /** Format: int64 */
            max_pages: number;
            /** @description 整数分；未知时为 null。 */
            max_price: number | null;
            /** @description 整数分；未知时为 null。 */
            min_price: number | null;
            name: string;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            next_run_at: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            no_good_hint_dismissed_at: number | null;
            notify_policy: string;
            notify_price_drop: boolean;
            personal_only: boolean;
            profile: components["schemas"]["AnalysisProfile"];
            publish_window: string;
            region_city: string | null;
            region_district: string | null;
            region_province: string | null;
            requirement: string;
            /** Format: int64 */
            search_generation: number;
            /** Format: int64 */
            sort_index: number;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            updated_at: number | null;
        };
        TaskFields: {
            account_id: number | null;
            account_pinned: boolean;
            analyze_on_first_run: boolean;
            /** @description 整数分；未知时为 null。 */
            budget_max: number | null;
            /** @description 整数分；未知时为 null。 */
            budget_min: number | null;
            channel_ids: number[];
            crawl_overrides: components["schemas"]["SettingValues"];
            enabled: boolean;
            exclude_keywords: string[];
            free_shipping_only: boolean;
            /** Format: int64 */
            interval_minutes: number;
            judge_mode: components["schemas"]["AnalysisMode"];
            keyword: string;
            match_keywords: string[];
            /** Format: int64 */
            max_pages: number;
            /** @description 整数分；未知时为 null。 */
            max_price: number | null;
            /** @description 整数分；未知时为 null。 */
            min_price: number | null;
            name: string;
            notify_policy: string;
            notify_price_drop: boolean;
            personal_only: boolean;
            profile: components["schemas"]["AnalysisProfile"];
            publish_window: string;
            region_city: string | null;
            region_district: string | null;
            region_province: string | null;
            requirement: string;
        };
        TaskItemSpecs: {
            brand_new: boolean | null;
            config_key: string | null;
            excluded: boolean;
            item_id: string;
            /** @description 直接复用 analysis.ItemSpecs；空字符串表示尚未判断商品种类，不用 JSON null 代替。 */
            kind: components["schemas"]["AnalysisKind"] | "";
            source: components["schemas"]["AnalysisSpecSource"];
            specs: components["schemas"]["AnalysisSpecValue"][];
            /** Format: int64 */
            task_id: number;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒。
             */
            updated_at: number;
        };
        TaskOverview: {
            average_duration_ms: number | null;
            judge_mode: components["schemas"]["AnalysisMode"];
            /** @description 整数分；未知时为 null。 */
            min_price: number | null;
            name: string;
            /** Format: int64 */
            new_finding_count: number;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            next_run_at: number | null;
            reason: components["schemas"]["LocalizedMessage"] | null;
            /** @description 比率，0.1 表示 10%；没有足够事实时为 null。 */
            recommendation_rate: number | null;
            /** Format: int64 */
            recommended_count: number;
            /** Format: int64 */
            scanned_count: number;
            state: components["schemas"]["TaskOverviewState"];
            /** @description 比率，0.1 表示 10%；没有足够事实时为 null。 */
            success_rate: number | null;
            /** Format: int64 */
            task_id: number;
            trend: components["schemas"]["AnalysisDailyPrice"][];
        };
        /** @enum {string} */
        TaskOverviewState: "monitoring" | "checking" | "error" | "paused";
        /** @enum {string} */
        TaskPhase: "idle" | "queued" | "running" | "finalizing";
        TaskRuntime: {
            account_id: number | null;
            blocked: components["schemas"]["RuntimeBlocked"] | null;
            phase: components["schemas"]["TaskPhase"];
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            queued_at: number | null;
            run_id: number | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            started_at: number | null;
            /** Format: int64 */
            task_id: number;
        };
        TaskSaveEffects: {
            added_exclude_words: string[];
            /** Format: int64 */
            existing_exclude_match_count: number;
            /** Format: int64 */
            generation: number;
            /** Format: int64 */
            needs_reanalysis_count: number;
            /** Format: int64 */
            rescored_count: number;
            search_reset: boolean;
            /** Format: int64 */
            title_rejected_count: number;
            /** @description 本次清单变更后缺少当前事实的精确发现编号集合，按 id 升序；长度与 needs_reanalysis_count 一致。Create 或无影响时返回 []，不为 null。 */
            needs_reanalysis_ids: number[];
            /** @description 二层因不匹配被拒的精确发现编号集合，按 id 升序；长度与 title_rejected_count 一致。Create 或无影响时返回 []，不为 null。 */
            title_rejected_ids: number[];
        };
        TaskSaveResult: {
            effects: components["schemas"]["TaskSaveEffects"];
            task: components["schemas"]["Task"];
            warnings: components["schemas"]["FieldIssue"][];
        };
        TaskSummary: {
            /** @description 整数分；未知时为 null。 */
            budget_max: number | null;
            /** @description 整数分；未知时为 null。 */
            budget_min: number | null;
            /** Format: int64 */
            id: number;
            judge_mode: components["schemas"]["AnalysisMode"];
            name: string;
            profile: components["schemas"]["AnalysisProfile"];
        };
        /** @description finding_counts 包含各 decision 键和 total；no_good_hint 未触发或已忽略时为 null。 */
        TaskView: {
            account_id: number | null;
            account_pinned: boolean;
            analyze_on_first_run: boolean;
            baseline_pending: boolean;
            /** @description 整数分；未知时为 null。 */
            budget_max: number | null;
            /** @description 整数分；未知时为 null。 */
            budget_min: number | null;
            channel_ids: number[];
            /** Format: int64 */
            consecutive_failures: number;
            crawl_overrides: components["schemas"]["SettingValues"];
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            created_at: number | null;
            enabled: boolean;
            exclude_keywords: string[];
            failure_paused_until: number | null;
            finding_counts: {
                [key: string]: number;
            };
            free_shipping_only: boolean;
            /** Format: int64 */
            id: number;
            /** Format: int64 */
            interval_minutes: number;
            judge_mode: components["schemas"]["AnalysisMode"];
            keyword: string;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            last_run_at: number | null;
            last_run_outcome: string | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            last_success_at: number | null;
            match_keywords: string[];
            /** Format: int64 */
            max_pages: number;
            /** @description 整数分；未知时为 null。 */
            max_price: number | null;
            /** @description 整数分；未知时为 null。 */
            min_price: number | null;
            name: string;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            next_run_at: number | null;
            no_good_hint: components["schemas"]["NoGoodHint"] | null;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            no_good_hint_dismissed_at: number | null;
            notify_policy: string;
            notify_price_drop: boolean;
            personal_only: boolean;
            profile: components["schemas"]["AnalysisProfile"];
            publish_window: string;
            region_city: string | null;
            region_district: string | null;
            region_province: string | null;
            requirement: string;
            runtime: components["schemas"]["TaskRuntime"];
            /** Format: int64 */
            search_generation: number;
            /** Format: int64 */
            sort_index: number;
            /** @description UTC Unix 整数毫秒；未知时为 null。 */
            updated_at: number | null;
        };
        Viewport: {
            /** Format: int64 */
            height: number;
            /** Format: int64 */
            width: number;
            /** Format: int64 */
            x: number;
            /** Format: int64 */
            y: number;
        };
        AnalysisProfileFields: {
            allowed_kinds: components["schemas"]["AnalysisKind"][];
            bonuses: components["schemas"]["AnalysisRequirement"][];
            musts: components["schemas"]["AnalysisRequirement"][];
            /** Format: int64 */
            next_no: number;
            risks: components["schemas"]["AnalysisProfileRisk"][];
            specs: components["schemas"]["AnalysisProfileSpec"][];
            summary: string;
        };
        AnalysisProfileSpecInput: {
            edited?: boolean;
            name?: string;
            options?: string[];
            source?: components["schemas"]["AnalysisProfileSource"];
            values?: string[];
        };
        AnalysisRequirementInput: {
            edited?: boolean;
            id?: string;
            /** Format: int64 */
            rev?: number;
            source?: components["schemas"]["AnalysisProfileSource"];
            text?: string;
        };
        AnalysisProfileRiskInput: {
            attitude?: components["schemas"]["AnalysisAttitude"];
            edited?: boolean;
            hint?: string;
            id?: string;
            name?: string;
            question?: string;
            /** Format: int64 */
            rev?: number;
            source?: components["schemas"]["AnalysisProfileSource"];
        };
        /** @description 整体替换清单。缺失成员按空值规范化；编号水位和语义版本由服务端维护，不接受请求回拨。AI 模式必须保留至少一项规格或必须条件。 */
        AnalysisProfileInput: {
            allowed_kinds?: components["schemas"]["AnalysisKind"][];
            bonuses?: components["schemas"]["AnalysisRequirementInput"][];
            musts?: components["schemas"]["AnalysisRequirementInput"][];
            /** Format: int64 */
            next_no?: number;
            risks?: components["schemas"]["AnalysisProfileRiskInput"][];
            specs?: components["schemas"]["AnalysisProfileSpecInput"][];
            summary?: string;
        };
        /** @description 未提供字段使用当前设置和领域默认。name 缺失时取 keyword 前 30 字；channel_ids 缺失选择所有启用渠道，显式 [] 不选。 */
        TaskCreate: {
            account_id?: number | null;
            account_pinned?: boolean;
            analyze_on_first_run?: boolean;
            budget_max?: number | null;
            budget_min?: number | null;
            channel_ids?: number[];
            /** @description 仅这九项任务覆盖；缺失项跟随全局设置，数值形状和范围由设置模块唯一校验。 */
            crawl_overrides?: {
                [key: string]: components["schemas"]["JsonValue"];
            };
            enabled?: boolean;
            exclude_keywords?: string[];
            free_shipping_only?: boolean;
            /** Format: int64 */
            interval_minutes?: number;
            judge_mode?: components["schemas"]["AnalysisMode"];
            keyword: string;
            match_keywords?: string[];
            /** Format: int64 */
            max_pages?: number;
            max_price?: number | null;
            min_price?: number | null;
            name?: string;
            /**
             * @description AI 只接受 steal/good/fair/all_new/none；关键词只接受 recommended/all_new/none。切换模式且未显式提供时，旧值无效则取新模式默认。
             * @enum {string}
             */
            notify_policy?: "steal" | "good" | "fair" | "all_new" | "none" | "recommended";
            notify_price_drop?: boolean;
            personal_only?: boolean;
            profile?: components["schemas"]["AnalysisProfileInput"];
            /** @enum {string} */
            publish_window?: "any" | "day1" | "day3" | "day7" | "day14";
            region_city?: string | null;
            region_district?: string | null;
            region_province?: string | null;
            requirement?: string;
        };
        /** @description 字段缺失保持；nullable 字段显式 null 清空。profile/crawl_overrides 整体替换；搜索条件变更与代次更新同一事务提交。 */
        TaskPatch: {
            account_id?: number | null;
            account_pinned?: boolean;
            analyze_on_first_run?: boolean;
            budget_max?: number | null;
            budget_min?: number | null;
            channel_ids?: number[];
            /** @description 仅这九项任务覆盖；缺失项跟随全局设置，数值形状和范围由设置模块唯一校验。 */
            crawl_overrides?: {
                [key: string]: components["schemas"]["JsonValue"];
            };
            enabled?: boolean;
            exclude_keywords?: string[];
            free_shipping_only?: boolean;
            /** Format: int64 */
            interval_minutes?: number;
            judge_mode?: components["schemas"]["AnalysisMode"];
            keyword?: string;
            match_keywords?: string[];
            /** Format: int64 */
            max_pages?: number;
            max_price?: number | null;
            min_price?: number | null;
            name?: string;
            /**
             * @description AI 只接受 steal/good/fair/all_new/none；关键词只接受 recommended/all_new/none。切换模式且未显式提供时，旧值无效则取新模式默认。
             * @enum {string}
             */
            notify_policy?: "steal" | "good" | "fair" | "all_new" | "none" | "recommended";
            notify_price_drop?: boolean;
            personal_only?: boolean;
            profile?: components["schemas"]["AnalysisProfileInput"];
            /** @enum {string} */
            publish_window?: "any" | "day1" | "day3" | "day7" | "day14";
            region_city?: string | null;
            region_district?: string | null;
            region_province?: string | null;
            requirement?: string;
        };
        AccountCreate: {
            name: string;
            proxy_id?: number | null;
        };
        AccountPatch: {
            name?: string;
            proxy_id?: number | null;
        };
        ProxyCreate: {
            name: string;
            /** @description 含可选认证信息的 http:// 或 socks5:// 代理地址；必须有端口，只写入加密存储，不回传。 */
            url: string;
        };
        ProxyPatch: {
            name?: string;
            /** @description 含可选认证信息的 http:// 或 socks5:// 代理地址；必须有端口，只写入加密存储，不回传。 */
            url?: string;
        };
        ConfirmVerificationRequest: {
            session_id: string;
        };
        ApplyExclusionsRequest: {
            added_exclude_words: string[];
        };
        FindingReadPatch: {
            is_read: boolean;
        };
        ListingPatch: {
            is_favorite?: boolean;
            is_dismissed?: boolean;
        };
        FindingSelection: {
            /** @description 正整数发现编号；服务端先稳定去重，按去重后的唯一编号计数，要求 1–200 个。重复编号不会增加 requested 或额外执行动作。 */
            ids: number[];
        };
        FindingBatchRequest: {
            /** @enum {string} */
            action: "delete" | "reanalyze" | "dismiss";
            /** @description 正整数发现编号；服务端先稳定去重，按去重后的唯一编号计数，要求 1–200 个。重复编号不会增加 requested 或额外执行动作。 */
            ids: number[];
        };
        ItemSpecsRequest: {
            specs: components["schemas"]["AnalysisSpecValue"][];
            brand_new: boolean | null;
        };
        ExtraCheckRequest: {
            extra_finding: components["schemas"]["AnalysisExtraFinding"];
        };
        BlockedSellerRequest: {
            nick: string | null;
        };
        ClearBusinessDataRequest: {
            /** @constant */
            confirmation: "清除";
        };
        BudgetStatus: {
            blocked: boolean;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒；保护额度窗口使用同一持久时钟。
             */
            effective_now: number;
            /** Format: int64 */
            limit: number;
            /** @description UTC Unix 整数毫秒；保护额度窗口使用同一持久时钟。 */
            next_check_at: number | null;
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒；保护额度窗口使用同一持久时钟。
             */
            protected_start: number;
            /** Format: int64 */
            protected_used: number;
        };
        ChannelDeleteResult: {
            affected_task_ids: number[];
            /** Format: int64 */
            id: number;
        };
        ChannelTestResult: {
            /** Format: int64 */
            at: number;
            /** Format: int64 */
            channel_id: number;
            /** Format: int64 */
            delivery_id: number;
            error: components["schemas"]["LocalizedMessage"] | null;
            ok: boolean;
        };
        DeliveryItem: {
            /** Format: int64 */
            finding_id: number;
            task_id: number | null;
            title: string | null;
        };
        GenerateProfileInput: {
            current: components["schemas"]["AnalysisProfileInput"];
            keyword: string;
            requirement: string;
            task_id?: number | null;
        };
        /** @enum {string} */
        ModelClientJSONMode: "schema" | "object" | "text";
        /** @enum {string} */
        ModelClientKind: "title_screen" | "analysis" | "profile" | "market";
        ModelClientSchema: {
            name: string;
            schema: components["schemas"]["JsonValue"];
        };
        /** @enum {string} */
        NotifyType: "bark" | "telegram" | "ntfy" | "gotify" | "wecom" | "dingtalk" | "feishu" | "pushplus" | "serverchan" | "webhook" | "email";
        PromptView: {
            /** Format: int64 */
            asset_version: number;
            content: string;
            customized: boolean;
            default: string;
            kind: components["schemas"]["ModelClientKind"];
            required_keys: string[];
            schema: components["schemas"]["ModelClientSchema"] | null;
            /** @description 构造请求时附加在模板后的固定系统说明，不进入可编辑正文。 */
            readonly system_suffix: string;
            updated_at: number | null;
        };
        /** @description 创建和 PUT 使用同一完整输入，除 api_key 外各字段必填；preset/text_model 可明确 null，其他字段不接受 null。 */
        ProviderInput: {
            /** @description 省略或 null 保留原密钥；字符串整体替换，空字符串明确清空。 */
            api_key?: string | null;
            base_url: string;
            enabled: boolean;
            name: string;
            preset: string | null;
            text_model: string | null;
            vision_model: string;
        };
        ProviderSaveResult: {
            provider: components["schemas"]["ProviderView"];
            warnings: components["schemas"]["LocalizedMessage"][];
        };
        ProviderView: {
            api_key_set: boolean;
            base_url: string;
            enabled: boolean;
            error_at: number | null;
            error_state: components["schemas"]["LocalizedMessage"] | null;
            /** Format: int64 */
            id: number;
            name: string;
            preset: string | null;
            /** Format: int64 */
            sort_index: number;
            text_model: string | null;
            vision_model: string;
        };
        PurposeUsage: {
            purpose: string;
            total: components["schemas"]["UsageTotal"];
        };
        TaskUsage: {
            deleted: boolean;
            task_id: number | null;
            task_name: string | null;
            total: components["schemas"]["UsageTotal"];
        };
        TrialReport: {
            passed: boolean;
            reason: components["schemas"]["LocalizedMessage"] | null;
            text: components["schemas"]["TrialStepReport"];
            vision: components["schemas"]["TrialStepReport"];
        };
        TrialStepReport: {
            duration_ms: number | null;
            http_status: number | null;
            /** Format: int64 */
            input_tokens: number;
            json_mode: components["schemas"]["ModelClientJSONMode"] | null;
            model: string | null;
            /** Format: int64 */
            output_tokens: number;
            reason: components["schemas"]["LocalizedMessage"] | null;
            reasoning_tokens: number | null;
            state: components["schemas"]["TrialStepState"];
            thinking: boolean | null;
            usage_incomplete: boolean;
            warnings: components["schemas"]["LocalizedMessage"][];
        };
        /** @enum {string} */
        TrialStepState: "passed" | "failed" | "not_run";
        UsageReport: {
            by_purpose: components["schemas"]["PurposeUsage"][];
            by_task: components["schemas"]["TaskUsage"][];
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒；区间为 [from,until)，未知保留起点为 null。
             */
            from: number;
            incomplete: boolean;
            /** @description UTC Unix 整数毫秒；区间为 [from,until)，未知保留起点为 null。 */
            retained_from: number | null;
            timezone: string;
            total: components["schemas"]["UsageTotal"];
            /**
             * Format: int64
             * @description UTC Unix 整数毫秒；区间为 [from,until)，未知保留起点为 null。
             */
            until: number;
        };
        UsageTotal: {
            average_duration_ms: number | null;
            /** Format: int64 */
            calls: number;
            /** Format: int64 */
            duration_ms: number;
            /** Format: int64 */
            input_tokens: number;
            /** Format: int64 */
            missing_usage_calls: number;
            /** Format: int64 */
            ok_calls: number;
            /** Format: int64 */
            output_tokens: number;
            reasoning_tokens: number | null;
            /** @description 比率，0.1 表示 10%；没有分母时为 null。 */
            success_rate: number | null;
        };
        ChannelFields: {
            config: components["schemas"]["JsonValue"];
            enabled: boolean;
            /** Format: int64 */
            id: number;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            last_test_at: number | null;
            last_test_ok: boolean | null;
            name: string;
            secret_set: {
                [key: string]: boolean;
            };
            type: components["schemas"]["NotifyType"];
        };
        /** @description 该渠道的不敏感配置；显式 config 整体替换，未提供成员使用适配器默认或业务校验。 */
        ChannelBarkConfig: Record<string, never>;
        /** @description 字段省略保留旧值；可选空串清空，headers 整对象替换且 {} 清空；null 非法。 */
        ChannelBarkSecrets: {
            /** @description 只写入加密存储，不回传。 */
            url?: string;
        };
        ChannelBark: {
            config: components["schemas"]["ChannelBarkConfig"];
            enabled: boolean;
            /** Format: int64 */
            id: number;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            last_test_at: number | null;
            last_test_ok: boolean | null;
            name: string;
            secret_set: {
                [key: string]: boolean;
            };
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "bark";
        };
        CreateChannelBark: {
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "bark";
            name: string;
            /** @default true */
            enabled: boolean;
            config?: components["schemas"]["ChannelBarkConfig"];
            /** @description 只写入加密存储，不回传。 */
            secrets?: components["schemas"]["ChannelBarkSecrets"];
        };
        /** @description 该渠道的不敏感配置；显式 config 整体替换，未提供成员使用适配器默认或业务校验。 */
        ChannelTelegramConfig: {
            chat_id?: string;
            api_url?: string;
        };
        /** @description 字段省略保留旧值；可选空串清空，headers 整对象替换且 {} 清空；null 非法。 */
        ChannelTelegramSecrets: {
            /** @description 只写入加密存储，不回传。 */
            token?: string;
        };
        ChannelTelegram: {
            config: components["schemas"]["ChannelTelegramConfig"];
            enabled: boolean;
            /** Format: int64 */
            id: number;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            last_test_at: number | null;
            last_test_ok: boolean | null;
            name: string;
            secret_set: {
                [key: string]: boolean;
            };
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "telegram";
        };
        CreateChannelTelegram: {
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "telegram";
            name: string;
            /** @default true */
            enabled: boolean;
            config?: components["schemas"]["ChannelTelegramConfig"];
            /** @description 只写入加密存储，不回传。 */
            secrets?: components["schemas"]["ChannelTelegramSecrets"];
        };
        /** @description 该渠道的不敏感配置；显式 config 整体替换，未提供成员使用适配器默认或业务校验。 */
        ChannelNtfyConfig: Record<string, never>;
        /** @description 字段省略保留旧值；可选空串清空，headers 整对象替换且 {} 清空；null 非法。 */
        ChannelNtfySecrets: {
            /** @description 只写入加密存储，不回传。 */
            url?: string;
            /** @description 只写入加密存储，不回传。 */
            token?: string;
        };
        ChannelNtfy: {
            config: components["schemas"]["ChannelNtfyConfig"];
            enabled: boolean;
            /** Format: int64 */
            id: number;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            last_test_at: number | null;
            last_test_ok: boolean | null;
            name: string;
            secret_set: {
                [key: string]: boolean;
            };
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "ntfy";
        };
        CreateChannelNtfy: {
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "ntfy";
            name: string;
            /** @default true */
            enabled: boolean;
            config?: components["schemas"]["ChannelNtfyConfig"];
            /** @description 只写入加密存储，不回传。 */
            secrets?: components["schemas"]["ChannelNtfySecrets"];
        };
        /** @description 该渠道的不敏感配置；显式 config 整体替换，未提供成员使用适配器默认或业务校验。 */
        ChannelGotifyConfig: {
            url?: string;
        };
        /** @description 字段省略保留旧值；可选空串清空，headers 整对象替换且 {} 清空；null 非法。 */
        ChannelGotifySecrets: {
            /** @description 只写入加密存储，不回传。 */
            token?: string;
        };
        ChannelGotify: {
            config: components["schemas"]["ChannelGotifyConfig"];
            enabled: boolean;
            /** Format: int64 */
            id: number;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            last_test_at: number | null;
            last_test_ok: boolean | null;
            name: string;
            secret_set: {
                [key: string]: boolean;
            };
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "gotify";
        };
        CreateChannelGotify: {
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "gotify";
            name: string;
            /** @default true */
            enabled: boolean;
            config?: components["schemas"]["ChannelGotifyConfig"];
            /** @description 只写入加密存储，不回传。 */
            secrets?: components["schemas"]["ChannelGotifySecrets"];
        };
        /** @description 该渠道的不敏感配置；显式 config 整体替换，未提供成员使用适配器默认或业务校验。 */
        ChannelWecomConfig: Record<string, never>;
        /** @description 字段省略保留旧值；可选空串清空，headers 整对象替换且 {} 清空；null 非法。 */
        ChannelWecomSecrets: {
            /** @description 只写入加密存储，不回传。 */
            url?: string;
        };
        ChannelWecom: {
            config: components["schemas"]["ChannelWecomConfig"];
            enabled: boolean;
            /** Format: int64 */
            id: number;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            last_test_at: number | null;
            last_test_ok: boolean | null;
            name: string;
            secret_set: {
                [key: string]: boolean;
            };
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "wecom";
        };
        CreateChannelWecom: {
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "wecom";
            name: string;
            /** @default true */
            enabled: boolean;
            config?: components["schemas"]["ChannelWecomConfig"];
            /** @description 只写入加密存储，不回传。 */
            secrets?: components["schemas"]["ChannelWecomSecrets"];
        };
        /** @description 该渠道的不敏感配置；显式 config 整体替换，未提供成员使用适配器默认或业务校验。 */
        ChannelDingtalkConfig: Record<string, never>;
        /** @description 字段省略保留旧值；可选空串清空，headers 整对象替换且 {} 清空；null 非法。 */
        ChannelDingtalkSecrets: {
            /** @description 只写入加密存储，不回传。 */
            url?: string;
            /** @description 只写入加密存储，不回传。 */
            secret?: string;
        };
        ChannelDingtalk: {
            config: components["schemas"]["ChannelDingtalkConfig"];
            enabled: boolean;
            /** Format: int64 */
            id: number;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            last_test_at: number | null;
            last_test_ok: boolean | null;
            name: string;
            secret_set: {
                [key: string]: boolean;
            };
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "dingtalk";
        };
        CreateChannelDingtalk: {
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "dingtalk";
            name: string;
            /** @default true */
            enabled: boolean;
            config?: components["schemas"]["ChannelDingtalkConfig"];
            /** @description 只写入加密存储，不回传。 */
            secrets?: components["schemas"]["ChannelDingtalkSecrets"];
        };
        /** @description 该渠道的不敏感配置；显式 config 整体替换，未提供成员使用适配器默认或业务校验。 */
        ChannelFeishuConfig: Record<string, never>;
        /** @description 字段省略保留旧值；可选空串清空，headers 整对象替换且 {} 清空；null 非法。 */
        ChannelFeishuSecrets: {
            /** @description 只写入加密存储，不回传。 */
            url?: string;
            /** @description 只写入加密存储，不回传。 */
            secret?: string;
        };
        ChannelFeishu: {
            config: components["schemas"]["ChannelFeishuConfig"];
            enabled: boolean;
            /** Format: int64 */
            id: number;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            last_test_at: number | null;
            last_test_ok: boolean | null;
            name: string;
            secret_set: {
                [key: string]: boolean;
            };
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "feishu";
        };
        CreateChannelFeishu: {
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "feishu";
            name: string;
            /** @default true */
            enabled: boolean;
            config?: components["schemas"]["ChannelFeishuConfig"];
            /** @description 只写入加密存储，不回传。 */
            secrets?: components["schemas"]["ChannelFeishuSecrets"];
        };
        /** @description 该渠道的不敏感配置；显式 config 整体替换，未提供成员使用适配器默认或业务校验。 */
        ChannelPushplusConfig: {
            topic?: string;
        };
        /** @description 字段省略保留旧值；可选空串清空，headers 整对象替换且 {} 清空；null 非法。 */
        ChannelPushplusSecrets: {
            /** @description 只写入加密存储，不回传。 */
            token?: string;
        };
        ChannelPushplus: {
            config: components["schemas"]["ChannelPushplusConfig"];
            enabled: boolean;
            /** Format: int64 */
            id: number;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            last_test_at: number | null;
            last_test_ok: boolean | null;
            name: string;
            secret_set: {
                [key: string]: boolean;
            };
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "pushplus";
        };
        CreateChannelPushplus: {
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "pushplus";
            name: string;
            /** @default true */
            enabled: boolean;
            config?: components["schemas"]["ChannelPushplusConfig"];
            /** @description 只写入加密存储，不回传。 */
            secrets?: components["schemas"]["ChannelPushplusSecrets"];
        };
        /** @description 该渠道的不敏感配置；显式 config 整体替换，未提供成员使用适配器默认或业务校验。 */
        ChannelServerchanConfig: Record<string, never>;
        /** @description 字段省略保留旧值；可选空串清空，headers 整对象替换且 {} 清空；null 非法。 */
        ChannelServerchanSecrets: {
            /** @description 只写入加密存储，不回传。 */
            send_key?: string;
        };
        ChannelServerchan: {
            config: components["schemas"]["ChannelServerchanConfig"];
            enabled: boolean;
            /** Format: int64 */
            id: number;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            last_test_at: number | null;
            last_test_ok: boolean | null;
            name: string;
            secret_set: {
                [key: string]: boolean;
            };
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "serverchan";
        };
        CreateChannelServerchan: {
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "serverchan";
            name: string;
            /** @default true */
            enabled: boolean;
            config?: components["schemas"]["ChannelServerchanConfig"];
            /** @description 只写入加密存储，不回传。 */
            secrets?: components["schemas"]["ChannelServerchanSecrets"];
        };
        /** @description 该渠道的不敏感配置；显式 config 整体替换，未提供成员使用适配器默认或业务校验。 */
        ChannelWebhookConfig: {
            /**
             * @default POST
             * @enum {string}
             */
            method: "GET" | "POST";
            /** @description JSON 文本字符串，解码后必须是 JSON 对象；数值原文保留。省略沿原默认，null 或直接提交对象均非法，不自动 stringify 兼容。 */
            query_params?: string;
            /**
             * @default JSON
             * @enum {string}
             */
            content_type: "JSON" | "FORM";
            /** @description 任意合法 JSON 的文本字符串，数值原文保留；字符串文本可包含 JSON null，但 HTTP 字段本身不可为 null。省略沿原默认，不接受直接对象或自动 stringify 兼容。 */
            body_template?: string;
        };
        /** @description 字段省略保留旧值；可选空串清空，headers 整对象替换且 {} 清空；null 非法。 */
        ChannelWebhookSecrets: {
            /** @description 只写入加密存储，不回传。 */
            url?: string;
            /** @description 只写入加密存储，不回传。 */
            headers?: {
                [key: string]: string;
            };
        };
        ChannelWebhook: {
            config: components["schemas"]["ChannelWebhookConfig"];
            enabled: boolean;
            /** Format: int64 */
            id: number;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            last_test_at: number | null;
            last_test_ok: boolean | null;
            name: string;
            secret_set: {
                [key: string]: boolean;
            };
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "webhook";
        };
        CreateChannelWebhook: {
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "webhook";
            name: string;
            /** @default true */
            enabled: boolean;
            config?: components["schemas"]["ChannelWebhookConfig"];
            /** @description 只写入加密存储，不回传。 */
            secrets?: components["schemas"]["ChannelWebhookSecrets"];
        };
        /** @description 该渠道的不敏感配置；显式 config 整体替换，未提供成员使用适配器默认或业务校验。 */
        ChannelEmailConfig: {
            host?: string;
            /** Format: int64 */
            port?: number;
            /** @enum {string} */
            encryption?: "ssl" | "starttls" | "none";
            username?: string;
            from?: string;
            to?: string;
        };
        /** @description 字段省略保留旧值；可选空串清空，headers 整对象替换且 {} 清空；null 非法。 */
        ChannelEmailSecrets: {
            /** @description 只写入加密存储，不回传。 */
            password?: string;
        };
        ChannelEmail: {
            config: components["schemas"]["ChannelEmailConfig"];
            enabled: boolean;
            /** Format: int64 */
            id: number;
            last_error: components["schemas"]["LocalizedMessage"] | null;
            last_test_at: number | null;
            last_test_ok: boolean | null;
            name: string;
            secret_set: {
                [key: string]: boolean;
            };
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "email";
        };
        CreateChannelEmail: {
            /**
             * @description discriminator enum property added by openapi-typescript
             * @enum {string}
             */
            type: "email";
            name: string;
            /** @default true */
            enabled: boolean;
            config?: components["schemas"]["ChannelEmailConfig"];
            /** @description 只写入加密存储，不回传。 */
            secrets?: components["schemas"]["ChannelEmailSecrets"];
        };
        Channel: components["schemas"]["ChannelBark"] | components["schemas"]["ChannelTelegram"] | components["schemas"]["ChannelNtfy"] | components["schemas"]["ChannelGotify"] | components["schemas"]["ChannelWecom"] | components["schemas"]["ChannelDingtalk"] | components["schemas"]["ChannelFeishu"] | components["schemas"]["ChannelPushplus"] | components["schemas"]["ChannelServerchan"] | components["schemas"]["ChannelWebhook"] | components["schemas"]["ChannelEmail"];
        /** @description 先保存配置，保存本身不发送消息。enabled 缺省 true；type/name 必填，其余由该渠道校验。 */
        ChannelCreate: components["schemas"]["CreateChannelBark"] | components["schemas"]["CreateChannelTelegram"] | components["schemas"]["CreateChannelNtfy"] | components["schemas"]["CreateChannelGotify"] | components["schemas"]["CreateChannelWecom"] | components["schemas"]["CreateChannelDingtalk"] | components["schemas"]["CreateChannelFeishu"] | components["schemas"]["CreateChannelPushplus"] | components["schemas"]["CreateChannelServerchan"] | components["schemas"]["CreateChannelWebhook"] | components["schemas"]["CreateChannelEmail"];
        /** @description 缺失保持；config 整对象替换，secrets 逐键合并。type 改变不继承旧 config/secrets；顶层 null 非法。 */
        ChannelPatch: {
            type?: components["schemas"]["NotifyType"];
            name?: string;
            enabled?: boolean;
            config?: components["schemas"]["ChannelBarkConfig"] | components["schemas"]["ChannelTelegramConfig"] | components["schemas"]["ChannelNtfyConfig"] | components["schemas"]["ChannelGotifyConfig"] | components["schemas"]["ChannelWecomConfig"] | components["schemas"]["ChannelDingtalkConfig"] | components["schemas"]["ChannelFeishuConfig"] | components["schemas"]["ChannelPushplusConfig"] | components["schemas"]["ChannelServerchanConfig"] | components["schemas"]["ChannelWebhookConfig"] | components["schemas"]["ChannelEmailConfig"];
            /** @description 只写入加密存储，不回传。 */
            secrets?: components["schemas"]["ChannelBarkSecrets"] | components["schemas"]["ChannelTelegramSecrets"] | components["schemas"]["ChannelNtfySecrets"] | components["schemas"]["ChannelGotifySecrets"] | components["schemas"]["ChannelWecomSecrets"] | components["schemas"]["ChannelDingtalkSecrets"] | components["schemas"]["ChannelFeishuSecrets"] | components["schemas"]["ChannelPushplusSecrets"] | components["schemas"]["ChannelServerchanSecrets"] | components["schemas"]["ChannelWebhookSecrets"] | components["schemas"]["ChannelEmailSecrets"];
        };
        /** @description 相邻配置的当前商品差价和典型差价；金额字段为整数分。 */
        AnalysisAdjacentPrice: {
            from: string;
            /** Format: int64 */
            item_premium: number;
            lower_config_key: string;
            spec_name: string;
            to: string;
            /** Format: int64 */
            typical_premium: number;
        };
        AnalysisEffectiveCheck: {
            evidence: string | null;
            key: string;
            source: components["schemas"]["AnalysisEvidenceOrigin"];
            status: components["schemas"]["AnalysisCheckStatus"];
        };
        /** @enum {string} */
        AnalysisEvidenceOrigin: "text" | "image" | "program" | "user" | "unknown";
        /** @description 当前只读行情比较，as_of 为本次读取时点；只在 GetFinding 详情计算，列表和动作响应为 null。发现原 fair_* 是上次打分快照，不冒充历史样本编号。 */
        FindingMarketComparison: {
            adjacent_prices: components["schemas"]["AnalysisAdjacentPrice"][];
            /** Format: int64 */
            as_of: number;
            brand_new: boolean;
            config_key: string;
            /** @description 当前真正参与 14/30 天、去重、放宽配置及 IQR 后的样本池，item_ids 是实际编号。 */
            fair: components["schemas"]["AnalysisFairPrice"];
        };
        ProviderEnabledRequest: {
            enabled: boolean;
        };
        /** @description 完整服务商编号集合；必须存在、无重复且全覆盖，按此顺序在同一事务保存。 */
        ProviderOrderRequest: {
            ids: number[];
        };
        PromptSaveRequest: {
            content: string;
        };
        /** @description 模型服务直接返回安全草稿，建议已由唯一转换入口换为整数分；无效预算只置 null，不使清单失败、触发重试或加入 UI 警告。 */
        GeneratedDraft: {
            profile: components["schemas"]["AnalysisProfile"];
            suggestions: components["schemas"]["SuggestionsView"];
            warnings: components["schemas"]["LocalizedMessage"][];
        };
        AnalysisSellerResult: {
            bonus_eligible: boolean;
            checks: components["schemas"]["AnalysisEffectiveCheck"][];
            reasons: components["schemas"]["LocalizedMessage"][];
            type: components["schemas"]["AnalysisSellerType"];
        };
        /** @description 预算为非负整数分；从最短十进制元四舍五入到分，无效/负数/非有限/超范围单值为 null，原预算倒置时上下限均为 null。建议词和预算仅在用户显式应用后进入任务。 */
        SuggestionsView: {
            /** @description 整数分；缺失或弃用的建议为 null。 */
            budget_max: number | null;
            /** @description 整数分；缺失或弃用的建议为 null。 */
            budget_min: number | null;
            exclude_words: string[];
            keyword: string | null;
        };
        /** @description 完整文件预检通过后逐任务保存的真实结果。total=imported+skipped+failed+unprocessed；保存中断但报告存在时仍返回 200、complete=false、failure 原因及已保存数量，不自动重试整份文件。 */
        TaskImportReport: {
            complete: boolean;
            /** Format: int64 */
            failed: number;
            failure: components["schemas"]["LocalizedMessage"] | null;
            /** Format: int64 */
            imported: number;
            items: components["schemas"]["TaskImportItem"][];
            /** Format: int64 */
            skipped: number;
            /** Format: int64 */
            total: number;
            /** Format: int64 */
            unprocessed: number;
        };
        /** @description index 从 1 开始；task_id 仅已保存项有值。修正、推荐范围警告和字段错误分别使用真实 FieldIssue 数组，未知原因为 null。 */
        TaskImportItem: {
            corrections: components["schemas"]["FieldIssue"][];
            fields: components["schemas"]["FieldIssue"][];
            /** Format: int64 */
            index: number;
            reason: components["schemas"]["LocalizedMessage"] | null;
            /** @enum {string} */
            status: "imported" | "skipped" | "failed";
            task_id: number | null;
            warnings: components["schemas"]["FieldIssue"][];
        };
        TaskChannelRef: {
            name: string;
            type: string;
        };
        /** @description 完整验证备份后的安全预览，不包含文件路径、主密钥或解密后的内容；expires_at 为 UTC 毫秒。 */
        BackupRestorePreview: {
            /**
             * Format: int64
             * @description 票据到期时间，UTC Unix 整数毫秒。
             */
            expires_at: number;
            manifest: components["schemas"]["BackupManifest"];
            /** @description 仅本次准备返回的单次恢复票据，绑定当前身份和运行代；确认前仍须有效认证。 */
            ticket: string;
        };
        BackupManifest: {
            app_version: string;
            /**
             * Format: date-time
             * @description 备份文件原始清单中的 RFC3339 时间字符串；不伪造为 HTTP UTC 毫秒字段。
             */
            created_at: string;
            /** @constant */
            format: "kakatta.backup";
            kdf: components["schemas"]["BackupKDF"];
            /** Format: int64 */
            schema_version: number;
            /**
             * Format: int64
             * @constant
             */
            version: 1;
        };
        BackupKDF: {
            /**
             * Format: int64
             * @constant
             */
            memory_kib: 65536;
            /** @constant */
            name: "argon2id";
            /** @description 备份文件清单中用于派生的规范 Base64 盐，不是主密钥。 */
            salt: string;
            /**
             * Format: int64
             * @constant
             */
            threads: 1;
            /**
             * Format: int64
             * @constant
             */
            time: 3;
        };
        /** @description 只读取发布检查缓存，不因 GET 发起联网。已知版本、尝试与成功时间、下次检查和比较结果均按实际未知返回 null；stale 明确缓存过期。 */
        UpdateStatus: {
            current_version: string;
            enabled: boolean;
            error: components["schemas"]["LocalizedMessage"] | null;
            known_release: components["schemas"]["UpdateRelease"] | null;
            /** @description UTC Unix 整数毫秒；未知为 null。 */
            last_attempt_at: number | null;
            /** @description UTC Unix 整数毫秒；未知为 null。 */
            last_success_at: number | null;
            /** @description UTC Unix 整数毫秒；未知为 null。 */
            next_check_at: number | null;
            stale: boolean;
            update_available: boolean | null;
        };
        UpdateRelease: {
            url: string;
            version: string;
        };
        /** @description 任务文件的完整配置，不包含账号绑定、运行状态、搜索代次、发现或渠道凭据。 */
        TaskExportEntry: {
            name: string;
            keyword: string;
            enabled: boolean;
            min_price: number | null;
            max_price: number | null;
            personal_only: boolean;
            free_shipping_only: boolean;
            publish_window: string;
            /** Format: int64 */
            max_pages: number;
            /** Format: int64 */
            interval_minutes: number;
            crawl_overrides: components["schemas"]["SettingValues"];
            judge_mode: components["schemas"]["AnalysisMode"];
            requirement: string;
            profile: components["schemas"]["AnalysisProfile"];
            budget_min: number | null;
            budget_max: number | null;
            match_keywords: string[];
            exclude_keywords: string[];
            analyze_on_first_run: boolean;
            notify_policy: string;
            notify_price_drop: boolean;
            region: {
                province: string | null;
                city: string | null;
                district: string | null;
            };
            channels: components["schemas"]["TaskChannelRef"][];
        };
        /** @description UTF-8 任务 JSON 下载文件，包含全部任务；金额为元，profile 原样导出。网页下载应请求 blob，不把全量文件读成当前页面列表。 */
        TaskExportFile: {
            /** @constant */
            format: "kakatta.tasks";
            /**
             * Format: int64
             * @constant
             */
            version: 1;
            /**
             * Format: date-time
             * @description 任务文件中的 RFC3339 时间字符串。
             */
            exported_at: string;
            tasks: components["schemas"]["TaskExportEntry"][];
        };
        TaskImportUpload: {
            /**
             * Format: binary
             * @description UTF-8 kakatta.tasks 文件，上传原文件字节；完整格式预检先于保存。
             */
            file: Blob;
        };
        BackupExportRequest: {
            /** @description 本次加密备份的密码，不保存到日志或响应。 */
            password: string;
        };
        BackupRestoreUpload: {
            /**
             * Format: binary
             * @description 原始加密备份 ZIP 文件；不把整个文件读入内存。
             */
            file: Blob;
            /** @description 用于本次解密验证的备份密码，不回传。 */
            password: string;
        };
        BackupConfirmRequest: {
            /** @description 本次安全预览给出的单次恢复票据。 */
            ticket: string;
        };
        /** @description 恢复成功，所有旧会话和令牌失效，需要用备份中的认证资料重新登录。 */
        BackupRestored: {
            /** @constant */
            restored: true;
        };
    };
    responses: {
        /** @description JSON、字段或参数不合法。JSON 请求拒绝重复字段及未声明的属性，设置值映射仍由设置模块校验。 */
        BadRequest: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description 尚未登录、凭据不正确，或会话、令牌、挑战已失效。 */
        Unauthorized: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description 浏览器跨站写操作被拒绝。 */
        Forbidden: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description 指定对象不存在。 */
        NotFound: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description 当前初始化或两步验证状态不允许此操作。 */
        Conflict: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description 请求体过大。 */
        PayloadTooLarge: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description 请求内容类型不是受支持的 JSON。 */
        UnsupportedMediaType: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description 设置校验失败；fields 中列出每个字段的原因。 */
        ValidationFailed: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description 登录失败保护正在限制请求。 */
        TooManyRequests: {
            headers: {
                /** @description 重新尝试前至少等待的秒数。 */
                "Retry-After"?: number;
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description 服务暂时无法完成请求；不返回内部错误和敏感输入。 */
        InternalError: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description 服务或依赖当前不可用；门禁或存储失败不会放行外部动作。 */
        Unavailable: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
    };
    parameters: never;
    requestBodies: never;
    headers: never;
    pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
    getHealth: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 服务可用。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["HealthReady"];
                };
            };
            /** @description 服务不可用；不披露内部原因。 */
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["HealthUnavailable"];
                };
            };
            default: components["responses"]["InternalError"];
        };
    };
    getSetupStatus: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 只返回是否初始化及令牌到期时间；不返回令牌、用户名或设置。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SetupStatus"];
                };
            };
            default: components["responses"]["InternalError"];
        };
    };
    setupAdmin: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SetupRequest"];
            };
        };
        responses: {
            /** @description 建立管理员并登录。 */
            200: {
                headers: {
                    /** @description 认证成功后设置 HttpOnly、Path=/、SameSite=Lax 且无 Domain 的 Cookie；HTTP 用 kakatta_session，实际 HTTPS 或可信代理报告 HTTPS 时用 __Host-kakatta_session 并加 Secure。登录返回 totp_required 时不发 Cookie。 */
                    "Set-Cookie"?: string;
                    /** @description 敏感响应不得缓存。 */
                    "Cache-Control"?: "no-store";
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Me"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            429: components["responses"]["TooManyRequests"];
            default: components["responses"]["InternalError"];
        };
    };
    login: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["LoginRequest"];
            };
        };
        responses: {
            /** @description 已认证时发 Cookie；需要两步验证时只返回五分钟挑战。 */
            200: {
                headers: {
                    /** @description 认证成功后设置 HttpOnly、Path=/、SameSite=Lax 且无 Domain 的 Cookie；HTTP 用 kakatta_session，实际 HTTPS 或可信代理报告 HTTPS 时用 __Host-kakatta_session 并加 Secure。登录返回 totp_required 时不发 Cookie。 */
                    "Set-Cookie"?: string;
                    /** @description 敏感响应不得缓存。 */
                    "Cache-Control"?: "no-store";
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["LoginResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            429: components["responses"]["TooManyRequests"];
            default: components["responses"]["InternalError"];
        };
    };
    verifyLoginTOTP: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["TOTPLoginRequest"];
            };
        };
        responses: {
            /** @description 验证码消费与创建会话同时成功后才发 Cookie。 */
            200: {
                headers: {
                    /** @description 认证成功后设置 HttpOnly、Path=/、SameSite=Lax 且无 Domain 的 Cookie；HTTP 用 kakatta_session，实际 HTTPS 或可信代理报告 HTTPS 时用 __Host-kakatta_session 并加 Secure。登录返回 totp_required 时不发 Cookie。 */
                    "Set-Cookie"?: string;
                    /** @description 敏感响应不得缓存。 */
                    "Cache-Control"?: "no-store";
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Me"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            429: components["responses"]["TooManyRequests"];
            default: components["responses"]["InternalError"];
        };
    };
    logout: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 当前凭据已吊销。 */
            204: {
                headers: {
                    /** @description 退出后清除会话 Cookie；名称及安全属性随当前可信协议确定。 */
                    "Set-Cookie"?: string;
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            default: components["responses"]["InternalError"];
        };
    };
    getMe: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 当前身份。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Me"];
                };
            };
            401: components["responses"]["Unauthorized"];
            default: components["responses"]["InternalError"];
        };
    };
    getSettings: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 有效值、来源和推荐范围警告。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SettingsSnapshot"];
                };
            };
            401: components["responses"]["Unauthorized"];
            default: components["responses"]["InternalError"];
        };
    };
    patchSettings: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SettingsPatch"];
            };
        };
        responses: {
            /** @description 保存后的完整有效快照。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SettingsSnapshot"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            default: components["responses"]["InternalError"];
        };
    };
    listSettingDefinitions: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 直接返回设置定义数组。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SettingDefinition"][];
                };
            };
            401: components["responses"]["Unauthorized"];
            default: components["responses"]["InternalError"];
        };
    };
    listSecurityTokens: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 已存凭据只返回元数据。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TokenList"];
                };
            };
            401: components["responses"]["Unauthorized"];
            default: components["responses"]["InternalError"];
        };
    };
    createSecurityToken: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["CreateTokenRequest"];
            };
        };
        responses: {
            /** @description 创建成功，明文只返回本次。 */
            201: {
                headers: {
                    /** @description 敏感响应不得缓存。 */
                    "Cache-Control"?: "no-store";
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["IssuedToken"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            default: components["responses"]["InternalError"];
        };
    };
    revokeSecurityToken: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description 设备或接口令牌编号。 */
                id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 指定凭据已吊销。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            default: components["responses"]["InternalError"];
        };
    };
    changePassword: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ChangePasswordRequest"];
            };
        };
        responses: {
            /** @description 密码已更新，其他会话和令牌按安全规则吊销。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            default: components["responses"]["InternalError"];
        };
    };
    beginTOTPSetup: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["TOTPSetupRequest"];
            };
        };
        responses: {
            /** @description 本次绑定的临时密钥和 URI；尚未启用两步验证。 */
            200: {
                headers: {
                    /** @description 敏感响应不得缓存。 */
                    "Cache-Control"?: "no-store";
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TOTPSetup"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            default: components["responses"]["InternalError"];
        };
    };
    enableTOTP: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["TOTPEnableRequest"];
            };
        };
        responses: {
            /** @description 两步验证已启用。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            default: components["responses"]["InternalError"];
        };
    };
    disableTOTP: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["TOTPDisableRequest"];
            };
        };
        responses: {
            /** @description 两步验证已关闭。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            default: components["responses"]["InternalError"];
        };
    };
    streamEvents: {
        parameters: {
            query?: never;
            header?: {
                /** @description 上次接收的事件位置。 */
                "Last-Event-ID"?: string;
            };
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description SSE 事件流。数据使用事件类型及原因代码；保活、慢客户端和断线恢复由服务端统一处理。凭据吊销后不得继续接收业务数据。 */
            200: {
                headers: {
                    /** @description 敏感响应不得缓存。 */
                    "Cache-Control"?: "no-store";
                    [name: string]: unknown;
                };
                content: {
                    "text/event-stream": string;
                };
            };
            401: components["responses"]["Unauthorized"];
            default: components["responses"]["InternalError"];
        };
    };
    getOpenAPI: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 与仓库描述及编译时嵌入的内容一致。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["OpenAPIDocument"];
                };
            };
            401: components["responses"]["Unauthorized"];
            default: components["responses"]["InternalError"];
        };
    };
    listTasks: {
        parameters: {
            query?: {
                q?: string;
                enabled?: boolean;
                /** @description 每页条数。 */
                limit?: number;
                /** @description 服务端返回的游标，绑定相同筛选和排序；非法或过期格式返回 400。 */
                cursor?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PageTaskView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    createTask: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["TaskCreate"];
            };
        };
        responses: {
            /** @description 已创建。 */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TaskSaveResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getTaskDefaults: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TaskFields"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getTask: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                task_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TaskView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    deleteTask: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                task_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成，无响应正文。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    updateTask: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                task_id: number;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["TaskPatch"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TaskSaveResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getTaskRuntime: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                task_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TaskRuntime"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    runTask: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                task_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 已接纳；可能排队，不代表已完成。 */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["RunSubmission"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    cancelTask: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                task_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成，无响应正文。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    dismissNoGoodHint: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                task_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成，无响应正文。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    applyTaskExclusions: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                task_id: number;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ApplyExclusionsRequest"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["MutationResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getTaskMarket: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                task_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["MarketView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listTaskMarketSamples: {
        parameters: {
            query?: {
                config_key?: string;
                brand_new?: boolean;
                /** @description 每页条数。 */
                limit?: number;
                /** @description 服务端返回的游标，绑定相同筛选和排序；非法或过期格式返回 400。 */
                cursor?: string;
            };
            header?: never;
            path: {
                task_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PageMarketSampleView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listRiskDictionary: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["AnalysisRiskDefinition"][];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listRegions: {
        parameters: {
            query?: {
                /** @description 省略返回省级，非空返回该父级的下一层地区。 */
                parent?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["RegionNode"][];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listAccounts: {
        parameters: {
            query?: {
                /** @description 每页条数。 */
                limit?: number;
                /** @description 服务端返回的游标，绑定相同筛选和排序；非法或过期格式返回 400。 */
                cursor?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PageAccountView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    createAccount: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["AccountCreate"];
            };
        };
        responses: {
            /** @description 已创建。 */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["AccountView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getAccount: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                account_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["AccountView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    deleteAccount: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                account_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成，无响应正文。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    updateAccount: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                account_id: number;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["AccountPatch"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["AccountView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    logoutAccount: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                account_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["AccountView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    openAccountLogin: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                account_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 已接纳；可能排队，不代表已完成。 */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SessionView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    openAccountVerification: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                account_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 已接纳；可能排队，不代表已完成。 */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SessionView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    confirmAccountVerification: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                account_id: number;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ConfirmVerificationRequest"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["AccountView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getAccountSession: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                account_id: number;
                session_id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SessionView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    closeAccountSession: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                account_id: number;
                session_id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成，无响应正文。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listProxies: {
        parameters: {
            query?: {
                /** @description 每页条数。 */
                limit?: number;
                /** @description 服务端返回的游标，绑定相同筛选和排序；非法或过期格式返回 400。 */
                cursor?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PageProxyView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    createProxy: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ProxyCreate"];
            };
        };
        responses: {
            /** @description 已创建。 */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ProxyView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    deleteProxy: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                proxy_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成，无响应正文。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    updateProxy: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                proxy_id: number;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ProxyPatch"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ProxyView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    testProxy: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                proxy_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ProxyTestResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listFindings: {
        parameters: {
            query?: {
                task_id?: number;
                /** @description default 为 AI fair/good/steal、无法比价且非 avoid、keyword matched，并排除超预算/隐藏；other 为其余已有结论，pending 不入。 */
                view?: "default" | "other" | "all" | "hidden";
                decision?: components["schemas"]["AnalysisDecision"];
                price_drop?: boolean;
                over_budget?: boolean;
                q?: string;
                /** @description score 降序、空最后，再 found_at/id 降序；latest 为 found_at/id 降序。 */
                sort?: "score" | "latest";
                /** @description 每页条数。 */
                limit?: number;
                /** @description 服务端返回的游标，绑定相同筛选和排序；非法或过期格式返回 400。 */
                cursor?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PageFindingView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getFinding: {
        parameters: {
            query?: {
                task_id?: number;
                /** @description default 为 AI fair/good/steal、无法比价且非 avoid、keyword matched，并排除超预算/隐藏；other 为其余已有结论，pending 不入。 */
                view?: "default" | "other" | "all" | "hidden";
                decision?: components["schemas"]["AnalysisDecision"];
                price_drop?: boolean;
                over_budget?: boolean;
                q?: string;
                /** @description score 降序、空最后，再 found_at/id 降序；latest 为 found_at/id 降序。 */
                sort?: "score" | "latest";
            };
            header?: never;
            path: {
                finding_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["FindingView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    deleteFinding: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                finding_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成，无响应正文。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    updateFinding: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                finding_id: number;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["FindingReadPatch"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["FindingView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    deleteTaskFindings: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                task_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["MutationResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    previewReanalysis: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["FindingSelection"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ReanalysisPreview"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    batchFindings: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["FindingBatchRequest"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["BatchResult"];
                };
            };
            /** @description 重新分析请求已接纳；完成状态以发现读取为准。 */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ReanalysisResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    reanalyzeFinding: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                finding_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 已接纳；可能排队，不代表已完成。 */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ReanalysisResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    setRiskOverrides: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                finding_id: number;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["AnalysisUserOverrides"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["FindingView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    setItemSpecs: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                task_id: number;
                item_id: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ItemSpecsRequest"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ItemSpecsUpdateResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    addExtraCheck: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                finding_id: number;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ExtraCheckRequest"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TaskSaveResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listListings: {
        parameters: {
            query?: {
                favorite?: boolean;
                dismissed?: boolean;
                q?: string;
                /** @description 每页条数。 */
                limit?: number;
                /** @description 服务端返回的游标，绑定相同筛选和排序；非法或过期格式返回 400。 */
                cursor?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PageListingView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getListing: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                item_id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ListingView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    updateListing: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                item_id: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ListingPatch"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ListingView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listListingPrices: {
        parameters: {
            query?: {
                /** @description 每页条数。 */
                limit?: number;
                /** @description 服务端返回的游标，绑定相同筛选和排序；非法或过期格式返回 400。 */
                cursor?: string;
            };
            header?: never;
            path: {
                item_id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PagePricePoint"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getListingThumbnail: {
        parameters: {
            query?: {
                /** @description 使用这条发现对应的任务账号；省略时取商品最近一条发现。 */
                finding_id?: number;
            };
            header?: never;
            path: {
                item_id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 返回 JPEG 缩略图。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "image/jpeg": Blob;
                };
            };
            /** @description 商品暂时没有可用的缩略图。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            404: components["responses"]["NotFound"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listBlockedSellers: {
        parameters: {
            query?: {
                /** @description 每页条数。 */
                limit?: number;
                /** @description 服务端返回的游标，绑定相同筛选和排序；非法或过期格式返回 400。 */
                cursor?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PageBlockedSeller"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    blockSeller: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                seller_id: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["BlockedSellerRequest"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["BlockedSeller"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    unblockSeller: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                seller_id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成，无响应正文。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getOverview: {
        parameters: {
            query?: {
                /** @description 缺省 today；显式空值非法。 */
                period?: "today" | "7d" | "30d";
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["OverviewView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listRuns: {
        parameters: {
            query?: {
                task_id?: number;
                outcome?: components["schemas"]["RunOutcome"];
                failure_kind?: components["schemas"]["FailureKind"];
                q?: string;
                /** @description UTC 毫秒，含起点。 */
                from_at?: number;
                /** @description UTC 毫秒，不含终点。 */
                to_at?: number;
                /** @description 每页条数。 */
                limit?: number;
                /** @description 服务端返回的游标，绑定相同筛选和排序；非法或过期格式返回 400。 */
                cursor?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PageRunView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getRun: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                run_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["RunDetail"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listRunSteps: {
        parameters: {
            query?: {
                /** @description 每页条数。 */
                limit?: number;
                /** @description 服务端返回的游标，绑定相同筛选和排序；非法或过期格式返回 400。 */
                cursor?: string;
            };
            header?: never;
            path: {
                run_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PageRunStep"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getRunLogOverview: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["RunLogOverview"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listSystemEvents: {
        parameters: {
            query?: {
                resolved?: boolean;
                /** @description 每页条数。 */
                limit?: number;
                /** @description 服务端返回的游标，绑定相同筛选和排序；非法或过期格式返回 400。 */
                cursor?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PageSystemEvent"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getSystemStatus: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SystemStatus"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    clearImageCache: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["CleanupResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    clearBusinessData: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ClearBusinessDataRequest"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["CleanupResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    redirectRemoteSession: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                session_id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            308: {
                headers: {
                    /** @description 同源远程会话尾斜杠路径。 */
                    Location?: string;
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getRemoteViewer: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                session_id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "text/html": string;
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            /** @description 远程会话请求被拒绝或资源不可用。 */
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ErrorEnvelope"];
                    "text/plain": string;
                };
            };
            /** @description 远程会话请求被拒绝或资源不可用。 */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ErrorEnvelope"];
                    "text/plain": string;
                };
            };
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            /** @description 远程会话请求被拒绝或资源不可用。 */
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ErrorEnvelope"];
                    "text/plain": string;
                };
            };
            default: components["responses"]["InternalError"];
        };
    };
    getRemoteViewport: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                session_id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Viewport"] | null;
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            /** @description 远程会话请求被拒绝或资源不可用。 */
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ErrorEnvelope"];
                    "text/plain": string;
                };
            };
            /** @description 远程会话请求被拒绝或资源不可用。 */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ErrorEnvelope"];
                    "text/plain": string;
                };
            };
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            /** @description 远程会话请求被拒绝或资源不可用。 */
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ErrorEnvelope"];
                    "text/plain": string;
                };
            };
            default: components["responses"]["InternalError"];
        };
    };
    connectRemoteVNC: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                session_id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description WebSocket 已升级；后续为 RFB 二进制帧，无 JSON 响应。 */
            101: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            /** @description 远程会话请求被拒绝或资源不可用。 */
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ErrorEnvelope"];
                    "text/plain": string;
                };
            };
            /** @description 远程会话请求被拒绝或资源不可用。 */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ErrorEnvelope"];
                    "text/plain": string;
                };
            };
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            /** @description 远程会话请求被拒绝或资源不可用。 */
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ErrorEnvelope"];
                    "text/plain": string;
                };
            };
            default: components["responses"]["InternalError"];
        };
    };
    getRemoteAsset: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                session_id: string;
                asset_path: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/octet-stream": Blob;
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            /** @description 远程会话请求被拒绝或资源不可用。 */
            403: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ErrorEnvelope"];
                    "text/plain": string;
                };
            };
            /** @description 远程会话请求被拒绝或资源不可用。 */
            404: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ErrorEnvelope"];
                    "text/plain": string;
                };
            };
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            /** @description 远程会话请求被拒绝或资源不可用。 */
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ErrorEnvelope"];
                    "text/plain": string;
                };
            };
            default: components["responses"]["InternalError"];
        };
    };
    listChannels: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Channel"][];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    createChannel: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ChannelCreate"];
            };
        };
        responses: {
            /** @description 已创建。 */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Channel"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getChannel: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                channel_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Channel"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    deleteChannel: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                channel_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ChannelDeleteResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    updateChannel: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                channel_id: number;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ChannelPatch"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Channel"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    testChannel: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                channel_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ChannelTestResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listChannelDeliveries: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                channel_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["DeliverySummary"][];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listDeliveryItems: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                delivery_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["DeliveryItem"][];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listAIProviders: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ProviderView"][];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    createAIProvider: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ProviderInput"];
            };
        };
        responses: {
            /** @description 已创建。 */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ProviderSaveResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getAIProvider: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                provider_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ProviderView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    updateAIProvider: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                provider_id: number;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ProviderInput"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ProviderSaveResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    deleteAIProvider: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                provider_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成，无响应正文。 */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    setAIProviderEnabled: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                provider_id: number;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ProviderEnabledRequest"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ProviderView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    reorderAIProviders: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ProviderOrderRequest"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ProviderView"][];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    testAIProvider: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                provider_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TrialReport"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    listPrompts: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PromptView"][];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    savePrompt: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                kind: components["schemas"]["ModelClientKind"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["PromptSaveRequest"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PromptView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    restorePrompt: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                kind: components["schemas"]["ModelClientKind"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PromptView"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    generateProfile: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["GenerateProfileInput"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["GeneratedDraft"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getAIUsage: {
        parameters: {
            query?: {
                /** @description 省略取 today，显式空值非法。 */
                window?: "today" | "7d" | "30d";
                task_id?: number;
                purpose?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["UsageReport"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getAIBudget: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["BudgetStatus"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    exportTasks: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    /** @description attachment，带服务端生成的安全文件名。 */
                    "Content-Disposition"?: string;
                    /** @description 实际传输字节数。 */
                    "Content-Length"?: number;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TaskExportFile"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    importTasks: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "multipart/form-data": components["schemas"]["TaskImportUpload"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TaskImportReport"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    exportTaskFindings: {
        parameters: {
            query?: {
                /** @description default 为 AI fair/good/steal、无法比价且非 avoid、keyword matched，并排除超预算/隐藏；other 为其余已有结论，pending 不入。 */
                view?: "default" | "other" | "all" | "hidden";
                decision?: components["schemas"]["AnalysisDecision"];
                price_drop?: boolean;
                over_budget?: boolean;
                q?: string;
                /** @description score 降序、空最后，再 found_at/id 降序；latest 为 found_at/id 降序。 */
                sort?: "score" | "latest";
            };
            header?: never;
            path: {
                task_id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    /** @description attachment，带服务端生成的安全文件名。 */
                    "Content-Disposition"?: string;
                    /** @description 实际传输字节数。 */
                    "Content-Length"?: number;
                    [name: string]: unknown;
                };
                content: {
                    "text/csv": Blob;
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    exportRuns: {
        parameters: {
            query?: {
                task_id?: number;
                outcome?: components["schemas"]["RunOutcome"];
                failure_kind?: components["schemas"]["FailureKind"];
                q?: string;
                /** @description UTC 毫秒，含起点。 */
                from_at?: number;
                /** @description UTC 毫秒，不含终点。 */
                to_at?: number;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    /** @description attachment，带服务端生成的安全文件名。 */
                    "Content-Disposition"?: string;
                    /** @description 实际传输字节数。 */
                    "Content-Length"?: number;
                    [name: string]: unknown;
                };
                content: {
                    "text/plain": Blob;
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    getUpdateStatus: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["UpdateStatus"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    exportBackup: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["BackupExportRequest"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    /** @description attachment，带服务端生成的安全文件名。 */
                    "Content-Disposition"?: string;
                    /** @description 实际传输字节数。 */
                    "Content-Length"?: number;
                    [name: string]: unknown;
                };
                content: {
                    "application/zip": Blob;
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    prepareBackupRestore: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "multipart/form-data": components["schemas"]["BackupRestoreUpload"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["BackupRestorePreview"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
    confirmBackupRestore: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["BackupConfirmRequest"];
            };
        };
        responses: {
            /** @description 操作完成。 */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["BackupRestored"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["PayloadTooLarge"];
            415: components["responses"]["UnsupportedMediaType"];
            422: components["responses"]["ValidationFailed"];
            503: components["responses"]["Unavailable"];
            default: components["responses"]["InternalError"];
        };
    };
}
