<p align="center"><img src="assets/logo/kakatta.png" width="128" alt="Kakatta"></p>

<h1 align="center">Kakatta</h1>

<p align="center">かかった！</p>

[English](README.md) · [简体中文](README.zh-CN.md) · [日本語](README.ja.md)

闲鱼（Xianyu）で探している商品を見守ります。タスクごとに定期検索し、新着商品をルールで絞り込み、必要に応じてAIで分析して、見る価値のある商品を通知します。サーバー、NAS、パソコン上のDockerコンテナ1つで動かし、ブラウザーからWeb画面を開いて使います。

プロジェクトは開発中です。以下ではソースからのビルド方法と、正式版の公開後にイメージを使って導入する方法を説明します。[実装・検証の記録](docs/IMPLEMENTATION.md)と[プロジェクト仕様](docs/)も参照してください。

## Docker Composeで起動

Linuxコンテナに対応した `amd64` または `arm64` のDocker環境を使います。Docker Desktopでも動作します。コンテナ用に少なくとも1 GBのメモリーを確保してください。メモリーに余裕がない環境では、既定の設定を維持し、ブラウザーのページを一度に1つだけ開くことをおすすめします。

ソースから起動する場合は、リポジトリのディレクトリで `docker compose up -d` を実行します。初回は自動でビルドします。`http://127.0.0.1:8080` を開き、管理者のユーザー名とパスワードをそのまま設定します。停止には `docker compose down` を使い、データはそのまま残ります。ソースを更新した後は `docker compose up -d --build` を実行します。正確なコミット番号を記録する場合は、起動コマンドの前に `KAKATTA_BUILD_COMMIT=$(git rev-parse HEAD)` を付けます。

正式なイメージを使う場合は、以下の内容を独立したディレクトリの `compose.yaml` に保存します。同じディレクトリの `.env` に `KAKATTA_IMAGE=ghcr.io/seaony/kakatta:VERSION` を記入し、`VERSION` を [GitHub Releases](https://github.com/Seaony/Kakatta/releases) で公開された正確なバージョンに置き換えます。先頭の `v` は取り除きます。リリース時に案内するDocker Hubリポジトリも利用できます。ワークフローは両方のレジストリーのイメージに、正確なバージョン、メジャーバージョン、`latest` のタグを付けます。正確なバージョンに固定し、アップグレード時に明示的に変更することをおすすめします。

```yaml
services:
  kakatta:
    image: "${KAKATTA_IMAGE:?Set KAKATTA_IMAGE to a published Kakatta image and tag}"
    ports:
      - "127.0.0.1:8080:8080"
    environment:
      PUID:
      PGID:
      TZ: "${TZ:-Asia/Shanghai}"
    volumes:
      - ./data:/data
    extra_hosts:
      - "host.docker.internal:host-gateway"
    security_opt:
      - seccomp=unconfined
    stop_grace_period: 30s
    command: ["--data", "/data", "--listen", ":8080"]
```

```sh
docker compose up -d
docker compose logs kakatta
```

初回起動後、Dockerホストで `http://127.0.0.1:8080` を開きます。ホストがリモートにある場合は、SSHトンネルまたはHTTPSリバースプロキシ経由でアクセスします。最初の画面で管理者のユーザー名とパスワードを設定します。バックアップがある場合は、アカウントを作成する前にバックアップのパスワードを入力し、確認して復元することもできます。すでに管理者がいる場合、復元にはログインが必要です。データディレクトリとログは安全に保管してください。アカウントの作成後、初回ガイドでは、闲鱼へのQRコードログイン、任意のAI・通知先の設定、最初のタスクの作成を案内します。

`PUID` と `PGID` を設定しない場合、起動スクリプトはマウント先ディレクトリのユーザーIDとグループIDを自動で使い、IDが0の場合はそれぞれ1000に置き換えます。明示的に指定する必要がある場合だけ、この2つの変数を設定してください。起動スクリプトは `/data` 配下の所有者を修正してから、root以外のユーザーでKakattaとChromeを動かします。`TZ` はコンテナのタイムゾーンとアプリの初期タイムゾーンを設定します。初期設定後は設定画面からアプリのタイムゾーンを変更できます。Macではそのまま起動できます。所有者を明示する必要がある場合は `PUID=$(id -u) PGID=$(id -g) docker compose up -d` を使います。QRコードを読み取る前に、[コンテナの説明](docs/CONTAINER.md)に従ってローカルの動作確認を行ってください。エラー時のファイルの保存場所も同じ文書に記載しています。

`seccomp=unconfined` はDockerのシステムコール制限を緩め、Chrome自身のサンドボックスを維持できるようにします。コンテナの権限を絞る場合は、[chrome-seccomp.json](deploy/chrome-seccomp.json) をComposeファイルの隣に保存し、この設定を `seccomp=./chrome-seccomp.json` に変更します。Chromeのサンドボックスは有効なままにし、公開するポートはアプリのポートだけにしてください。

中国国内からGHCRのイメージを取得する際に時間がかかる場合は、Docker Hubの同じ正確なバージョンに切り替え、Dockerデーモンに信頼できるミラーを設定できます。`registry-mirrors` の設定はDocker Hubからの取得に適用されます。[Dockerのミラー設定](https://docs.docker.com/docker-hub/image-library/mirror/#configure-the-docker-daemon)

### ホスト上のOllamaに接続

ホスト上のOllamaのアドレスには `http://host.docker.internal:11434/v1` を指定します。ローカルのOllamaにAPIキーは不要です。Ollamaがホストのループバックアドレスだけで待ち受けていて、コンテナから接続できない場合は、`OLLAMA_HOST` でコンテナから届く待ち受けアドレスに変更し、アクセスできるクライアントを信頼できるものに限定してください。Ollamaが同じComposeネットワークにある場合は、アドレスにそのサービス名を使います。[Ollamaのネットワーク設定](https://docs.ollama.com/faq#how-can-i-expose-ollama-on-my-network)

## HTTPSとリモートアクセス

KakattaはHTTPを提供し、HTTPSはリバースプロキシで処理します。Kakatta用のドメインを用意し、`https://kakatta.example.com/` のようにルートパスからアクセスしてください。プロキシとDockerが同じホスト上にある場合は、ポートのバインド先をローカルのループバックアドレスのままにします。リアルタイムイベント用の `/api/v1/events` とリモート画面用の `/remote/sessions/` を含め、サイト全体をプロキシします。元のリクエストパスとホスト名を維持し、WebSocketへのアップグレードを許可して、SSEレスポンスのプロキシキャッシュとバッファリングを無効にしてください。

以下の例はDockerホスト上でプロキシを直接動かす構成です。プロキシを別のコンテナで動かす場合は、両方のコンテナを同じプライベートDockerネットワークに接続し、接続先のアドレスを `kakatta:8080` にします。

**設定 → セキュリティ → 信頼するリバースプロキシ** には、自分で管理するプロキシのIPアドレスまたは小さなCIDR範囲だけを追加します。`KAKATTA_TRUSTED_PROXIES` にカンマ区切りの一覧を指定することもでき、その場合は環境変数の値が優先されます。Kakattaから実際に見える直接の接続元アドレスを指定してください。Dockerのポートマッピングを使う場合は、ブリッジのゲートウェイになることがあります。信頼できないプログラムがそのアドレス経由でバックエンドにアクセスできない場合に限り、そのアドレスを信頼する設定にしてください。プロキシは実際の外部リクエストに合わせて `X-Forwarded-Host` と `X-Forwarded-Proto` を設定し、`X-Forwarded-For` を設定するか、既存の値に正しく追記する必要があります。Kakattaは、信頼する接続元から届いたこれらのヘッダーだけを使って、HTTPSとクライアントIPを判定します。

**Caddy**：ドメインをCaddyが動作するホストに向けます。CaddyはWebSocketへのアップグレードを処理し、`text/event-stream` レスポンスを逐次転送します。[Caddyのリバースプロキシ設定](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)

```caddyfile
kakatta.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

**Nginx**：以下の設定を `http` コンテキストに置き、自分の証明書のパスを指定します。バックアップを復元する際は、`server` ブロックの `client_max_body_size` をZIPとフォームの付加データを収められるサイズに設定してください。[NginxのWebSocket設定](https://nginx.org/en/docs/http/websocket.html) · [Nginxのプロキシ設定](https://nginx.org/en/docs/http/ngx_http_proxy_module.html)

```nginx
map $http_upgrade $kakatta_connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl;
    server_name kakatta.example.com;
    ssl_certificate     /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $http_host;
        proxy_set_header X-Forwarded-Host $http_host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $kakatta_connection_upgrade;
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_cache off;
        proxy_read_timeout 600s;
    }
}
```

**Cloudflare Tunnel**：Dockerホストで `cloudflared` を動かし、名前付きトンネルを作成して、公開ホスト名を `http://127.0.0.1:8080` に接続します。パス全体を維持し、WebSocketを許可し、アプリのキャッシュを無効にしてから、ブラウザーでHTTPSのドメインを開きます。一時的なQuick TunnelはSSEに対応していません。バックアップがトンネルのアップロード上限を超える場合は、ローカル接続に切り替えて復元してください。[Cloudflare Tunnelの設定](https://developers.cloudflare.com/tunnel/get-started/) · [Quick Tunnelの制限](https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/)

**Tailscale**：Dockerホストで `tailscale serve --bg http://127.0.0.1:8080` を実行し、表示されたHTTPSアドレスからアクセスします。サービスは自分のTailscaleネットワーク内だけで共有されます。[Tailscale Serveの説明](https://tailscale.com/docs/reference/tailscale-cli/serve)

プロキシの設定後、HTTPSでログインし、ブラウザーが受け取るセッションCookieに `Secure` が付いていることと、画面でリアルタイム更新を受け取れることを確認してください。リモート画面のWebSocketにも、同じドメインで接続できる必要があります。Chromeのデバッグポートとコンテナ内のVNCポートは外部に公開しないでください。

## データディレクトリと鍵

`./data` は永続化用のマウント先です。`kakatta.db`、既定の `secret.key`、`browser/` 内のブラウザープロファイル、画像キャッシュ、`backups/` 内のデータベーススナップショットが入ります。このディレクトリを保護し、インスタンスごとに独立したディレクトリを使ってください。元のディレクトリを使ってコンテナを作り直せば、状態を引き継げます。

新しいデータベースを作る際、サーバーは `secret.key` に 32バイトのマスターキーを生成します。対応するデータベースと一緒に保管してください。データディレクトリ全体が漏えいすると、鍵、暗号化した認証情報、ブラウザープロファイル内の闲鱼ログイン状態が一緒に渡る可能性があります。

鍵を分けて保管する場合は、鍵ファイルを読み取り専用でマウントし、`KAKATTA_SECRET_KEY_FILE` にコンテナ内のファイルパスを指定できます。ファイルは正確に 32バイトの生データで、サービスの実行ユーザーが読み取れる必要があります。既存のインスタンスを外部ファイルに切り替える場合は、元の鍵と同じ内容を使ってください。`KAKATTA_SECRET_KEY` に、この 32バイトを標準Base64でエンコードした値を指定することもできます。2つの変数はどちらか片方だけを設定します。Dockerの状態を調べる権限を持つ人はコンテナの環境変数を見られるため、ファイルでの指定をおすすめします。

## バックアップ・復元・アップグレード

**設定 → バックアップと復元** でバックアップのパスワードを設定し、暗号化したZIPを作成します。ファイルとパスワードはDockerホスト以外の場所に安全に保管してください。バックアップには、業務データ、設定、管理者のユーザー名、パスワードハッシュ、2段階認証の設定、暗号化したマスターキーが含まれます。闲鱼のブラウザーセッション、KakattaのWebセッションとAPIトークン、アクセス回数の記録、画像キャッシュは含まれません。

1. 別の環境に移す場合は、移行先のインスタンスを起動し、バックアップ画面を開けるように、まず管理者の初期設定を済ませます。移行先のバージョンは、バックアップのデータベーススキーマのバージョンに対応している必要があります。
2. 移行先でZIPをアップロードし、バックアップのパスワードを入力します。検証後にバックアップの情報を確認し、全体の上書きを確定します。移行先の既存の業務データは置き換わります。外部の鍵を設定している場合は、復元する認証情報を移行先の鍵で暗号化し直します。
3. バックアップ内の管理者パスワードと元の認証アプリでログインし直します。APIトークンを作り直し、タスクと設定を確認してから、闲鱼に1アカウントずつQRコードでログインし直します。復元中に接続が切れた場合は、移行先の状態を確認してから再試行してください。

イメージをアップグレードする前に、リリースノートを確認してバックアップを書き出します。Kakattaを停止してからデータディレクトリ全体のコピーを別に保存し、古いイメージのタグを控えてください。外部の鍵を使っている場合は、対応する鍵も保管します。`KAKATTA_IMAGE` を更新先のバージョンに変更し、以下のコマンドを順に実行します。

```sh
docker compose pull kakatta
docker compose up -d kakatta
```

起動時にデータベースを自動で移行します。移行が必要な場合は、`data/backups/` にアップグレード前のデータベーススナップショットを作成します。元のバージョンに戻す場合は、まずKakattaを停止し、アップグレード後のデータディレクトリを別の場所に移してから、古いイメージのタグ、アップグレード前のデータのコピー、対応する鍵で起動します。バックアップ後の変更は、移動したディレクトリに残ります。古いバージョンでは新しいデータベーススキーマを開けません。古いChromeでも、ブラウザープロファイルの作り直しとQRコードでの再ログインが必要になることがあります。更新内容を確認したうえで、手動でアップグレードすることをおすすめします。

Kakattaのパスワードを忘れた場合は、`docker compose exec --user kakatta kakatta kakatta reset-password --data /data` を実行し、案内に従って新しいパスワードを入力します。この操作でKakattaの全セッションとAPIトークンが無効になります。認証アプリも使えなくなった場合は、同じコマンドの末尾に `--disable-totp` を付けます。2段階認証だけを無効にする場合は、`docker compose exec --user kakatta kakatta kakatta disable-totp --data /data` を実行します。

## APIとMCP

**設定 → アプリ情報と更新** でAPIドキュメントを確認するか、`/api/v1/openapi.json` をダウンロードします。スクリプトやMCPクライアントから使う場合は、**設定 → セキュリティ** でAPIトークンを作成し、`Authorization: Bearer <トークン>` を送信します。MCPクライアントのStreamable HTTPアドレスには、Kakattaのアドレスに `/mcp` を付けたものを指定します。ツールは結果の確認だけでなく、タスクの作成、一時停止、再開、実行もできます。トークンは信頼できるクライアントだけに渡してください。不要になったらセキュリティ画面で取り消せます。

## アカウントの保護

Kakattaは闲鱼の閲覧だけを行い、注文、メッセージの送信、ブラウザーのフィンガープリントの変更はしません。認証が必要になると停止し、Kakattaにログインした後のリモート画面で手動で対処します。使い始めは、既定のアクセス回数の上限を維持することをおすすめします。アクセス頻度を制限することはできますが、闲鱼側でアカウントが制限される可能性は残ります。購入前に商品の根拠をご自身で確認してください。モデルのスコアは参考情報です。[取得ルール](docs/CRAWL-POLICY.md)

## ローカル開発

[go.mod](go.mod) で指定するGoと [package.json](package.json) で指定するBunを使います。ローカルにGoogle Chromeをインストールし、リポジトリのルートで実行します。

```sh
bun run dev
```

`http://127.0.0.1:5173` を開きます。初回はサンプルを自動で作成し、ローカルの模擬闲鱼、模擬モデル、模擬通知先を使います。ログインは不要です。データの保存先は `build/_dev/data` に固定しています。タスク画面で「示例 · MacBook监控」を手動実行すると、一連の処理を確認できます。Web画面のパネルでバックエンドのログと直近のエラーを確認でき、ログイン状態の失効、アクセス制限、フィールドの欠落、APIエラーに切り替えられます。Web画面の変更は即時に反映します。Goの変更はAirがコンパイルしてから自動で切り替えます。実行中のタスクは正常に終わるまで待ち、コンパイルに失敗した場合は古いサービスを使い続けます。Ctrl+Cを1回押すと、安全に終了して子プロセスも終了します。

作者が実際のサイトを検証する場合は `bun run dev:real` を実行します。Go、Chrome、Airは開発用コンテナ内で、Viteはホスト上で動作します。`./data` にある正式環境のログイン状態と保護記録を引き継ぎ、新しいデータディレクトリでは最初の画面でユーザー名とパスワードを設定します。起動するたびに自動実行のスケジュールは一時停止した状態になり、画面で手動実行するか、明示的にスケジュールを有効にするまでタスクは始まりません。Goを変更した場合も、実行中の処理が終わるまで待ってから切り替え、ログイン状態とアクセス回数の記録は消しません。初回のビルドでは開発ツールと依存パッケージのダウンロードが必要です。開発用イメージは正式版のイメージより大きく、コンパイルにも追加のメモリーが必要です。[開発用コンテナ](docs/CONTAINER.md#开发热更新)を参照してください。

すべてのチェックを実行する場合は、以下を使います。

```sh
bun install --cwd web --frozen-lockfile
bun run --cwd web check
go build ./...
go test ./...
go test -race ./...
go vet ./...
```

`go run ./cmd/seed --data <empty-directory>` で、架空のデータを独立した空のディレクトリに書き込めます。開発にはローカルの模擬闲鱼と模擬サービスを使い、[コンテナの回帰テスト](deploy/test-container.sh)はネットワークを切断した環境で実行します。実際のアカウントでのログイン、認証、公開サイトのページの動作は、引き続きメンテナーによる検証が必要です。[実験計画](docs/EXPERIMENT.md)

## ライセンス

[MIT](LICENSE)。[参考プロジェクト](docs/REFERENCES.md)と[同梱のnoVNC](internal/remote/README.md)は、それぞれのライセンスに従います。
