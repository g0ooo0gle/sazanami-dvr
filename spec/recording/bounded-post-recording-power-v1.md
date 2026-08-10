# 録画後電源動作仕様 v1

- Status: Accepted
- Date: 2026-08-10
- Applies to: Sazanami DVR v0.0.28
- Decision: `docs/adr/0048-bounded-linux-post-recording-power.md`

## 目的

KonomiTV v0.14.1の録画後電源動作を予約、自動予約、SQLite、録画終了後のLinux動作で同じ意味にする。同時録画や次の予約がある状態では電源を切らない。

## 保存値とCtrlCmd

| 保存値 | `suspend_mode` | `reboot_flag` | Linux動作 |
|---|---:|---:|---|
| `DEFAULT` | 0 | false | 現在の全体既定である「何もしない」 |
| `NOTHING` | 4 | false | 何もしない |
| `STANDBY` | 1 | false | suspend |
| `STANDBY_REBOOT` | 1 | true | suspendから復帰後にreboot |
| `SUSPEND` | 2 | false | hibernate |
| `SUSPEND_REBOOT` | 2 | true | hibernateから復帰後にreboot |
| `SHUTDOWN` | 3 | false | poweroff |

2013と2015は表の値だけを受け付ける。`suspend_mode`が0または3または4で`reboot_flag=true`、5以上、truncated、末尾byteは要求全体を失敗させる。2011は保存値を同じwire値へ戻す。

自動予約条件は同じ7種類を保存して読み戻す。予約作成時にも同じ値を引き継ぐ。電源動作を理由に規則全体を判定不能にしない。

## SQLite schema 12

`reservations`へ`post_power_mode`を追加する。0は電源動作なし、1から5は表の電源動作順とする。型はINTEGER、範囲は0から5、既定値は0である。

既存の`post_action_mode`は、0を`DEFAULT`、1を`NOTHING`として残す。`post_power_mode`が1以上なら`post_action_mode`は0でなければならない。INSERTと二項目のUPDATEで制約を確認する。

schema 11から12への更新前にonline backupを作る。backupをschema 11として検証できなければ更新しない。通常起動では自動migrationしない。

## 電源動作の候補

正常終了した完成録画と、利用者停止で完成名へ公開した再生可能な部分録画だけが候補を返す。完成ファイルの公開、directory同期、DB終了確定、録画後スクリプトをこの順に終えてから返す。

開始前停止、録画失敗、容量不足、通信切断後の失敗、プロセス停止、再起動復旧では候補を返さない。電源動作の失敗で録画結果、完成ファイル、履歴を変更しない。

## 複数録画と次の予約

schedulerは候補をメモリ内に一時保持する。実行中の録画がある間は待つ。同じ待機期間に異なる電源動作が集まった場合は競合として全候補を破棄する。

実行中録画が0件になったら、DBから次の有効予約を読む。次の予定開始まで10分未満なら実行しない。10分以上なら予定開始5分前を復帰時刻とする。復帰時刻が現在から5分未満なら実行しない。次の予約がなければ復帰時刻を設定しない。

候補は成功、失敗、競合、時刻不足のいずれでも一回で破棄する。別queue、永続化、自動再試行は追加しない。

## Linux adapter

process開始時に`systemctl`と`rtcwake`を`exec.LookPath`で一度だけ解決し、絶対pathへ固定する。解決できない場合も通常起動は続けるが、電源動作の実行は固定理由で失敗する。

次の予約がある場合、最初に次を実行する。

```text
rtcwake --mode no --time <UTCのUnix秒>
```

成功後、動作に応じて次の一つを実行する。

```text
systemctl --no-ask-password suspend
systemctl --no-ask-password hibernate
systemctl --no-ask-password poweroff
```

復帰後再起動では、suspendまたはhibernateが戻った後に次を実行する。

```text
systemctl --no-ask-password reboot
```

電源要求が失敗してprocessへ戻った場合と、再起動を要求しない状態で復帰した場合は、`rtcwake --mode disable`を一回実行する。解除失敗は診断するが再試行しない。

shell、`sudo`、相対path、実行時のPATH検索、任意引数、環境変数の引継ぎを使わない。標準入力を閉じ、標準出力と標準エラーを破棄し、command内容を通常logへ出さない。実行はschedulerの同じGo routineで行い、追加のworkerを作らない。

## 固定診断

- `post-recording-power-conflict`
- `post-recording-power-too-late`
- `post-recording-power-unavailable`
- `post-recording-wake-failed`
- `post-recording-power-failed`
- `post-recording-reboot-failed`
- `post-recording-wake-clear-failed`
- `post-recording-power-cancelled`

通常logには結果と固定理由だけを出す。番組名、予約名、放送情報、実行ファイルpath、引数、標準出力、標準エラー、接続先を含めない。

## 必須テスト

- 7種類のdomain検証、2013／2015／2011往復、自動予約の保存・読戻し・予約作成への引継ぎ。
- `suspend_mode` 0から255と`reboot_flag`の組合せ、truncated、末尾byte、部分保存なし。
- schema 11から12のbackup付きmigration、既定値、全値roundtrip、再起動、restore、再migration、future、drift、型・範囲・二項目制約。
- 正常完了と利用者停止だけが候補を返し、失敗、開始前停止、process停止、復旧は返さない。
- 同時録画1から8件、同一候補、異なる候補、完了順、候補なし、終了と起動の競合、一件超過。
- 次の予約なし、10分境界、1ミリ秒内外、5分前の復帰、UTC、時刻overflow、DB失敗、取消し。
- command解決失敗、RTC成功・失敗・取消し、各systemctl動作、復帰後再起動、解除成功・失敗、非0終了、signal、出力破棄、引数、環境、追加goroutineなし。
- 既存の予約、録画、録画後スクリプト、複数録画、自動予約、録画履歴、再生、ライブの回帰確認。
- full、shuffle、race、vet、`go mod verify`、`govulncheck`、CGO無効のLinux／Darwin amd64／arm64 build、Hosted Ubuntu CI。
- 公開Linux amd64配布物を実験環境へ導入し、KonomiTVで全7種類を追加・変更・読戻しする。実際のスタンバイは、RTC復帰と予約余裕を読み取り専用で確認できた場合だけ行う。休止、復帰後再起動、シャットダウンは、別途安全な復帰手段がない限り実行せず、設定往復と無害なcommand adapter試験までとする。
