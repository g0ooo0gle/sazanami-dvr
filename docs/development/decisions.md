[README](../../README.md) / [開発者向け](README.md) / 設計判断

# 設計判断

現在の実装に直接関係する判断を案内します。

| 判断 | 内容 |
|---|---|
| [ADR-0067](../adr/0067-konomitv-practical-backend-goal.md) | KonomiTVの日常的な録画・予約操作を対象にする |
| [ADR-0062](../adr/0062-docker-compose-deployment.md) | Docker Composeの標準構成 |
| [ADR-0070](../adr/0070-streamlined-installation-entrypoints.md) | Linuxのインストールと削除 |
| [ADR-0051](../adr/0051-versioned-linux-installation.md) | Linux配置と版管理 |
| [ADR-0049](../adr/0049-default-listener-ports.md) | CtrlCmd、録画HTTP、WebUIの既定ポート |
| [ADR-0058](../adr/0058-channel-map-standard-path.md) | チャンネル設定の標準配置 |
| [ADR-0034](../adr/0034-bounded-ctrlcmd-live-relay.md) | 上限付きCtrlCmdライブ中継 |
| [ADR-0057](../adr/0057-komorebi-original-live-hls.md) | Komorebi向け原画質HLS |
| [ADR-0033](../adr/0033-completed-recording-read-surface.md) | 完成録画の一覧、詳細、再生 |
| [ADR-0037](../adr/0037-user-stopped-recording-publication.md) | 録画中の利用者停止と部分録画 |
| [ADR-0064](../adr/0064-konomitv-recording-delete-reconciliation.md) | 録画ファイル削除後の履歴整合 |
| [ADR-0069](../adr/0069-practical-release-quality.md) | リリース品質の確認範囲 |

新しい仕様や実装境界を決める場合は、影響範囲を確認してから必要なADRを追加または更新します。ADRだけでは実装や実環境確認の完了を意味しません。
