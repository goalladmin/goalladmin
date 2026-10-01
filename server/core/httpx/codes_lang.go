package httpx

// 错误码默认文案的其他语言（D-026）。简体中文和英文写在 codes.go 里；这里缺的码按 FallbackLangs 回退。
// 由机器辅助翻译，交付前建议请母语使用者校对。

// langMessages 返回某种语言的错误码文案；没有时返回 nil（按 FallbackLangs 回退）。
func langMessages(lang string) map[int]string {
	switch lang {
	case LangZHTW:
		//nolint:gosec,misspell // G101 误报（错误码名里有 Token、Pwd，这是提示文案不是凭据）；各语言的拼写不按英文检查
		return map[int]string{
			CodeOK:                "成功",
			CodeLoginFailed:       "帳號或密碼錯誤",
			CodeCaptchaRequired:   "請輸入驗證碼",
			CodeLocked:            "登入已暫時鎖定，請稍後再試",
			CodeTokenInvalid:      "尚未登入或登入已過期",
			CodeRefreshRetry:      "重新整理發生衝突，請重試",
			CodeSessionLocked:     "螢幕已鎖定，請先解鎖",
			CodeForbidden:         "沒有權限執行此操作",
			CodePwdChangeRequired: "請先變更密碼",
			CodeValidation:        "參數驗證失敗",
			CodeBadRequest:        "請求格式錯誤",
			CodeConflict:          "資源已存在或狀態衝突",
			CodeLastSuper:         "不能停用或降級最後一位超級管理員",
			CodeDeclaredInCode:    "此內容由程式碼宣告，無法在後台修改或刪除",
			CodeNotFound:          "資源不存在",
			CodeMethodNotAllowed:  "不允許此方法",
			CodeBodyTooLarge:      "請求內容過大",
			CodeTooManyRequests:   "請求過於頻繁，請稍後再試",
			CodeInternal:          "伺服器內部錯誤",
			CodeUnavailable:       "服務暫時無法使用",
		}
	case LangJA:
		//nolint:gosec,misspell // G101 误报（错误码名里有 Token、Pwd，这是提示文案不是凭据）；各语言的拼写不按英文检查
		return map[int]string{
			CodeOK:                "成功",
			CodeLoginFailed:       "アカウントまたはパスワードが正しくありません",
			CodeCaptchaRequired:   "認証コードを入力してください",
			CodeLocked:            "ログインが一時的にロックされています。しばらくしてから再試行してください",
			CodeTokenInvalid:      "ログインしていないか、ログインの有効期限が切れています",
			CodeRefreshRetry:      "更新が競合しました。再試行してください",
			CodeSessionLocked:     "画面がロックされています。先にロックを解除してください",
			CodeForbidden:         "この操作を実行する権限がありません",
			CodePwdChangeRequired: "先にパスワードを変更してください",
			CodeValidation:        "入力内容の検証に失敗しました",
			CodeBadRequest:        "リクエストの形式が正しくありません",
			CodeConflict:          "リソースが既に存在するか、状態が競合しています",
			CodeLastSuper:         "最後のスーパー管理者は無効化または降格できません",
			CodeDeclaredInCode:    "この内容はコードで宣言されているため、管理画面で変更・削除できません",
			CodeNotFound:          "リソースが存在しません",
			CodeMethodNotAllowed:  "許可されていないメソッドです",
			CodeBodyTooLarge:      "リクエスト本文が大きすぎます",
			CodeTooManyRequests:   "リクエストが多すぎます。しばらくしてから再試行してください",
			CodeInternal:          "サーバー内部エラーが発生しました",
			CodeUnavailable:       "サービスは一時的に利用できません",
		}
	case LangKO:
		//nolint:gosec,misspell // G101 误报（错误码名里有 Token、Pwd，这是提示文案不是凭据）；各语言的拼写不按英文检查
		return map[int]string{
			CodeOK:                "성공",
			CodeLoginFailed:       "계정 또는 비밀번호가 올바르지 않습니다",
			CodeCaptchaRequired:   "인증 코드를 입력하십시오",
			CodeLocked:            "로그인이 일시적으로 잠겼습니다. 잠시 후 다시 시도하십시오",
			CodeTokenInvalid:      "로그인하지 않았거나 로그인이 만료되었습니다",
			CodeRefreshRetry:      "갱신 충돌이 발생했습니다. 다시 시도하십시오",
			CodeSessionLocked:     "화면이 잠겨 있습니다. 먼저 잠금을 해제하십시오",
			CodeForbidden:         "이 작업을 수행할 권한이 없습니다",
			CodePwdChangeRequired: "먼저 비밀번호를 변경하십시오",
			CodeValidation:        "파라미터 검증에 실패했습니다",
			CodeBadRequest:        "요청 형식이 올바르지 않습니다",
			CodeConflict:          "리소스가 이미 존재하거나 상태가 충돌합니다",
			CodeLastSuper:         "마지막 최고 관리자는 사용 중지하거나 권한을 낮출 수 없습니다",
			CodeDeclaredInCode:    "코드에서 선언한 항목이므로 여기서 수정하거나 삭제할 수 없습니다",
			CodeNotFound:          "리소스가 존재하지 않습니다",
			CodeMethodNotAllowed:  "허용되지 않는 메서드입니다",
			CodeBodyTooLarge:      "요청 본문이 너무 큽니다",
			CodeTooManyRequests:   "요청이 너무 많습니다. 잠시 후 다시 시도하십시오",
			CodeInternal:          "서버 내부 오류가 발생했습니다",
			CodeUnavailable:       "서비스를 일시적으로 사용할 수 없습니다",
		}
	case LangMS:
		//nolint:gosec,misspell // G101 误报（错误码名里有 Token、Pwd，这是提示文案不是凭据）；各语言的拼写不按英文检查
		return map[int]string{
			CodeOK:                "Berjaya",
			CodeLoginFailed:       "Nama pengguna atau kata laluan salah",
			CodeCaptchaRequired:   "Sila masukkan kod pengesahan",
			CodeLocked:            "Log masuk dikunci buat sementara. Sila cuba lagi kemudian",
			CodeTokenInvalid:      "Belum log masuk atau sesi telah tamat",
			CodeRefreshRetry:      "Konflik semasa muat semula. Sila cuba lagi",
			CodeSessionLocked:     "Skrin dikunci; sila buka kunci dahulu",
			CodeForbidden:         "Tiada kebenaran untuk melakukan operasi ini",
			CodePwdChangeRequired: "Sila tukar kata laluan dahulu",
			CodeValidation:        "Pengesahan parameter gagal",
			CodeBadRequest:        "Format permintaan tidak sah",
			CodeConflict:          "Sumber sudah wujud atau berlaku konflik status",
			CodeLastSuper:         "Pentadbir super terakhir tidak boleh dinyahdayakan atau diturunkan",
			CodeDeclaredInCode:    "Kandungan ini diisytiharkan dalam kod dan tidak boleh diubah atau dipadam di sini",
			CodeNotFound:          "Sumber tidak wujud",
			CodeMethodNotAllowed:  "Kaedah tidak dibenarkan",
			CodeBodyTooLarge:      "Badan permintaan terlalu besar",
			CodeTooManyRequests:   "Terlalu banyak permintaan. Sila cuba lagi kemudian",
			CodeInternal:          "Ralat dalaman pelayan",
			CodeUnavailable:       "Perkhidmatan tidak tersedia buat sementara",
		}
	case LangTA:
		//nolint:gosec,misspell // G101 误报（错误码名里有 Token、Pwd，这是提示文案不是凭据）；各语言的拼写不按英文检查
		return map[int]string{
			CodeOK:                "வெற்றி",
			CodeLoginFailed:       "பயனர்பெயர் அல்லது கடவுச்சொல் தவறு",
			CodeCaptchaRequired:   "சரிபார்ப்புக் குறியீட்டை உள்ளிடவும்",
			CodeLocked:            "உள்நுழைவு தற்காலிகமாகப் பூட்டப்பட்டுள்ளது. பின்னர் மீண்டும் முயலவும்",
			CodeTokenInvalid:      "உள்நுழையவில்லை அல்லது அமர்வு காலாவதியானது",
			CodeRefreshRetry:      "புதுப்பிப்பில் முரண்பாடு. மீண்டும் முயலவும்",
			CodeSessionLocked:     "திரை பூட்டப்பட்டுள்ளது; முதலில் திறக்கவும்",
			CodeForbidden:         "இந்தச் செயலைச் செய்ய உங்களுக்கு அனுமதி இல்லை",
			CodePwdChangeRequired: "முதலில் கடவுச்சொல்லை மாற்றவும்",
			CodeValidation:        "அளவுருச் சரிபார்ப்பு தோல்வியடைந்தது",
			CodeBadRequest:        "கோரிக்கையின் வடிவம் தவறானது",
			CodeConflict:          "வளம் ஏற்கெனவே உள்ளது அல்லது நிலை முரண்பாடு",
			CodeLastSuper:         "கடைசி முதன்மை நிர்வாகியை முடக்கவோ தரம் குறைக்கவோ முடியாது",
			CodeDeclaredInCode:    "இது நிரலில் அறிவிக்கப்பட்டது; இங்கே மாற்றவோ நீக்கவோ முடியாது",
			CodeNotFound:          "வளம் இல்லை",
			CodeMethodNotAllowed:  "இந்த முறை அனுமதிக்கப்படவில்லை",
			CodeBodyTooLarge:      "கோரிக்கை உள்ளடக்கம் மிகப் பெரியது",
			CodeTooManyRequests:   "கோரிக்கைகள் மிக அதிகம். பின்னர் மீண்டும் முயலவும்",
			CodeInternal:          "சேவையக உள் பிழை",
			CodeUnavailable:       "சேவை தற்காலிகமாகக் கிடைக்கவில்லை",
		}
	case LangBN:
		//nolint:gosec,misspell // G101 误报（错误码名里有 Token、Pwd，这是提示文案不是凭据）；各语言的拼写不按英文检查
		return map[int]string{
			CodeOK:                "সফল",
			CodeLoginFailed:       "ইউজারনেম বা পাসওয়ার্ড ভুল",
			CodeCaptchaRequired:   "ক্যাপচা লিখুন",
			CodeLocked:            "লগইন সাময়িকভাবে লক করা হয়েছে, পরে আবার চেষ্টা করুন",
			CodeTokenInvalid:      "লগইন করা নেই বা লগইনের মেয়াদ শেষ",
			CodeRefreshRetry:      "রিফ্রেশে দ্বন্দ্ব হয়েছে, আবার চেষ্টা করুন",
			CodeSessionLocked:     "স্ক্রিন লক করা আছে; আগে আনলক করুন",
			CodeForbidden:         "এই কাজটি করার অনুমতি নেই",
			CodePwdChangeRequired: "অনুগ্রহ করে আগে পাসওয়ার্ড পরিবর্তন করুন",
			CodeValidation:        "প্যারামিটার যাচাই ব্যর্থ হয়েছে",
			CodeBadRequest:        "অনুরোধের ফরম্যাট সঠিক নয়",
			CodeConflict:          "রিসোর্সটি ইতিমধ্যে আছে বা অবস্থায় দ্বন্দ্ব রয়েছে",
			CodeLastSuper:         "শেষ সুপার অ্যাডমিনকে নিষ্ক্রিয় বা পদাবনত করা যায় না",
			CodeDeclaredInCode:    "এটি কোডে ঘোষিত, অ্যাডমিন থেকে পরিবর্তন বা মুছে ফেলা যায় না",
			CodeNotFound:          "রিসোর্সটি নেই",
			CodeMethodNotAllowed:  "এই মেথড অনুমোদিত নয়",
			CodeBodyTooLarge:      "অনুরোধের বডি খুব বড়",
			CodeTooManyRequests:   "অনুরোধ খুব ঘন ঘন, পরে আবার চেষ্টা করুন",
			CodeInternal:          "সার্ভারের অভ্যন্তরীণ ত্রুটি",
			CodeUnavailable:       "সেবা সাময়িকভাবে পাওয়া যাচ্ছে না",
		}
	case LangRU:
		//nolint:gosec,misspell // G101 误报（错误码名里有 Token、Pwd，这是提示文案不是凭据）；各语言的拼写不按英文检查
		return map[int]string{
			CodeOK:                "Успешно",
			CodeLoginFailed:       "Неверный логин или пароль",
			CodeCaptchaRequired:   "Введите код с картинки",
			CodeLocked:            "Вход временно заблокирован, повторите попытку позже",
			CodeTokenInvalid:      "Вы не вошли в систему или сеанс истёк",
			CodeRefreshRetry:      "Конфликт обновления, повторите попытку",
			CodeSessionLocked:     "Экран заблокирован; сначала разблокируйте его",
			CodeForbidden:         "Нет прав на выполнение этой операции",
			CodePwdChangeRequired: "Сначала смените пароль",
			CodeValidation:        "Ошибка проверки параметров",
			CodeBadRequest:        "Некорректный формат запроса",
			CodeConflict:          "Ресурс уже существует или конфликт состояния",
			CodeLastSuper:         "Нельзя отключить или понизить последнего суперадминистратора",
			CodeDeclaredInCode:    "Объявлено в коде, изменить или удалить здесь нельзя",
			CodeNotFound:          "Ресурс не найден",
			CodeMethodNotAllowed:  "Метод не разрешён",
			CodeBodyTooLarge:      "Слишком большое тело запроса",
			CodeTooManyRequests:   "Слишком много запросов, повторите попытку позже",
			CodeInternal:          "Внутренняя ошибка сервера",
			CodeUnavailable:       "Сервис временно недоступен",
		}
	case LangFR:
		//nolint:gosec,misspell // G101 误报（错误码名里有 Token、Pwd，这是提示文案不是凭据）；各语言的拼写不按英文检查
		return map[int]string{
			CodeOK:                "Succès",
			CodeLoginFailed:       "Identifiant ou mot de passe incorrect",
			CodeCaptchaRequired:   "Veuillez saisir le code de vérification",
			CodeLocked:            "Connexion temporairement verrouillée, veuillez réessayer plus tard",
			CodeTokenInvalid:      "Non connecté ou session expirée",
			CodeRefreshRetry:      "Conflit d’actualisation, veuillez réessayer",
			CodeSessionLocked:     "Écran verrouillé ; déverrouillez-le d’abord",
			CodeForbidden:         "Vous n’avez pas le droit d’effectuer cette opération",
			CodePwdChangeRequired: "Veuillez d’abord changer votre mot de passe",
			CodeValidation:        "Échec de la validation des paramètres",
			CodeBadRequest:        "Format de requête incorrect",
			CodeConflict:          "La ressource existe déjà ou son état est en conflit",
			CodeLastSuper:         "Impossible de désactiver ou de rétrograder le dernier super-administrateur",
			CodeDeclaredInCode:    "Cet élément est déclaré dans le code et ne peut pas être modifié ni supprimé ici",
			CodeNotFound:          "Ressource introuvable",
			CodeMethodNotAllowed:  "Méthode non autorisée",
			CodeBodyTooLarge:      "Corps de la requête trop volumineux",
			CodeTooManyRequests:   "Trop de requêtes, veuillez réessayer plus tard",
			CodeInternal:          "Erreur interne du serveur",
			CodeUnavailable:       "Service temporairement indisponible",
		}
	case LangDE:
		//nolint:gosec,misspell // G101 误报（错误码名里有 Token、Pwd，这是提示文案不是凭据）；各语言的拼写不按英文检查
		return map[int]string{
			CodeOK:                "Erfolgreich",
			CodeLoginFailed:       "Benutzername oder Passwort falsch",
			CodeCaptchaRequired:   "Bitte geben Sie das Captcha ein",
			CodeLocked:            "Anmeldung vorübergehend gesperrt. Bitte versuchen Sie es später erneut",
			CodeTokenInvalid:      "Nicht angemeldet oder Sitzung abgelaufen",
			CodeRefreshRetry:      "Aktualisierungskonflikt. Bitte versuchen Sie es erneut",
			CodeSessionLocked:     "Bildschirm gesperrt; bitte zuerst entsperren",
			CodeForbidden:         "Keine Berechtigung für diesen Vorgang",
			CodePwdChangeRequired: "Bitte ändern Sie zuerst Ihr Passwort",
			CodeValidation:        "Parameterprüfung fehlgeschlagen",
			CodeBadRequest:        "Ungültiges Anfrageformat",
			CodeConflict:          "Ressource existiert bereits oder Statuskonflikt",
			CodeLastSuper:         "Der letzte Super-Administrator kann nicht deaktiviert oder herabgestuft werden",
			CodeDeclaredInCode:    "Dieser Inhalt ist im Code deklariert und kann hier nicht geändert oder gelöscht werden",
			CodeNotFound:          "Ressource existiert nicht",
			CodeMethodNotAllowed:  "Methode nicht zulässig",
			CodeBodyTooLarge:      "Anfragetext zu groß",
			CodeTooManyRequests:   "Zu viele Anfragen. Bitte versuchen Sie es später erneut",
			CodeInternal:          "Interner Serverfehler",
			CodeUnavailable:       "Dienst vorübergehend nicht verfügbar",
		}
	default:
		return nil
	}
}
