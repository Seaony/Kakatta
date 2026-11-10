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
}
