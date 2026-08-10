# 録画後スクリプト仕様 v1

- Status: Accepted
- Date: 2026-08-10
- Applies to: Sazanami DVR v0.0.22
- Decision: `docs/adr/0043-bounded-post-recording-script.md`

## 目的

KonomiTV v0.14.1の「デフォルト」「何もしない」と録画後スクリプトを、予約、SQLite、実行結果で同じ意味にする。LANから受け取った任意pathを実行しない。

## 予約の値

予約は次の録画後設定を持つ。

- `DEFAULT`: 全体既定を使う。v1の実効値は何もしない。
- `NOTHING`: 明示的に何もしない。
- `SCRIPT`: 空または許可済みの絶対path。動作値とは独立し、`DEFAULT`または`NOTHING`と同時に指定できる。

スクリプトpathはUTF-8で1から1,024 byteとし、NULと制御文字を含めない。空文字は指定なしを表す。予約追加、変更、自動予約からの作成は、ほかの録画設定と同じtransactionで保存する。

## CtrlCmd

2013と2015は次を受け付ける。

| `suspend_mode` | `reboot_flag` | 保存値 |
|---:|---:|---|
| 0 | false | `DEFAULT` |
| 4 | false | `NOTHING` |

`suspend_mode`が1、2、3、4かつ`reboot_flag=true`、0かつ`reboot_flag=true`、5以上の値は`recording-setting-out-of-profile`で要求全体を失敗させる。予約を部分保存しない。

`bat_file_path`が空ならスクリプト指定なしとする。値があれば許可ディレクトリと対象ファイルを検証する。条件を満たさない場合は同じ失敗理由で予約全体を保存しない。

2011は保存した動作を上表のwire値へ戻し、スクリプトpathを同じ文字列で返す。`DEFAULT`と`NOTHING`を丸めない。truncated、末尾byte、未知値は成功させない。

## 許可ディレクトリ

既定の許可ディレクトリは`<data-root>/post-recording-scripts`である。`recording serve`の明示optionで別の絶対pathへ変更できる。起動時に次を確認する。

- directoryがなければmode 700で作成する。
- symlink、通常ファイル、利用者本人以外が書込み可能なdirectoryは拒否する。
- 解決後の絶対pathをprocessの存続中は固定する。

対象スクリプトは予約保存時と実行直前に確認する。pathの各要素にsymlinkを許可せず、解決後も許可ディレクトリ直下または子directory内でなければならない。通常ファイルで、実行bitが一つ以上あり、現在の利用者が実行できない場合は拒否する。

## 実行

完成ファイルの公開、directory同期、部分ファイル削除、DBの終了確定がすべて成功した後に一回だけ実行する。対象は次の二つである。

- 正常終了した完成録画。
- 利用者停止で完成名へ公開した再生可能な部分録画。

失敗、開始前停止、容量不足、stream不正、process停止、再起動復旧では実行しない。実行直前の検証に失敗した場合も起動しない。

実行はshellを介さず、スクリプト自身を引数なしで開始する。環境変数は次だけを渡す。

- `PATH=/usr/bin:/bin`
- `SAZANAMI_RECORDING_NUMBER`: 正の予約番号。
- `SAZANAMI_RECORDING_FILE`: 完成ファイルの絶対path。
- `SAZANAMI_RECORDING_STATE`: `SUCCEEDED`または`PARTIAL`。
- `SAZANAMI_RECORDING_REASON`: 固定終了理由。

標準入力は閉じ、標準出力と標準エラーは破棄する。timeoutは5分で、一件につき最大一processとする。自動再試行、shell、glob、環境変数展開、PATH検索は行わない。process終了後は必ずwaitし、子processを残さない。

## 失敗時の扱い

スクリプトの起動失敗、非0終了、timeout、実行中のSazanami停止は、完成済み録画の状態とファイルを変更しない。通常診断には次の固定理由だけを出し、script path、録画path、出力内容を含めない。

- `post-recording-script-invalid`
- `post-recording-script-start-failed`
- `post-recording-script-exit-failed`
- `post-recording-script-timeout`
- `post-recording-script-cancelled`

スクリプト失敗で常駐processと次の予約を停止しない。再起動後に同じスクリプトを自動実行し直さない。

## SQLite schema 11

`reservations`へ`post_action_mode`と`post_script_path`を追加する。動作は0を`DEFAULT`、1を`NOTHING`とする。pathは空文字または1,024 byte以内のUTF-8とし、型、範囲、既定値をCHECK制約で確認する。旧予約は`DEFAULT`と空pathへ移行する。

schema 10から11への更新前backupを必須とする。backupをschema 10として復元できなければ更新完了にしない。通常起動で自動migrationしない。

## 不変条件

- 完成ファイルと録画履歴を確定する前に外部processを起動しない。
- CtrlCmdの任意path、symlink、許可ディレクトリ外のfileを実行しない。
- shell、無制限の実行時間、無制限のprocess、再試行queueを追加しない。
- スクリプトの失敗で完成録画を削除、変更、失敗扱いにしない。
- スクリプトpath、録画path、出力を通常logへ出さない。
- 電源動作、録画TS、ライブ視聴、番組表を変更しない。

## 必須テスト

- `DEFAULT`、`NOTHING`、空path、許可pathのdomain検証と2013、2015、2011の往復。
- `suspend_mode` 0から5、`reboot_flag`の全組み合わせ、1,024 byte境界、制御文字、truncated、末尾byte。
- 許可directoryの作成とmode、外側path、共通prefix、`..`、絶対・相対、symlink、directory、非実行file、差替え。
- 成功、非0終了、起動失敗、5分timeout、取消し、process停止、同時録画8件、一件超過、子processとfile descriptorの残留。
- 正常完成と利用者停止だけで実行し、開始前停止、失敗、再起動復旧では実行しない。
- 環境変数、引数なし、標準入出力、pathと出力の非log、失敗後の次予約継続。
- 自動予約の対応値を作成予約へ引き継ぎ、未対応の電源動作では作成しない。
- schema 10→11、既定値、roundtrip、再起動、backup、restore、再migration、future、drift。
- full、shuffle、race、vet、`go mod verify`、`govulncheck`、CGO無効のLinux／Darwin amd64／arm64 build、Hosted Ubuntu CI。
- 公開Linux amd64配布物でKonomiTV相当の設定往復と、無害な試験スクリプトの一回実行を確認する。
