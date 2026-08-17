/* 多语言:加载语言包并应用 data-i18n */
const I18N = {
  lang: "zh",
  dict: {},
  async init() {
    this.lang = localStorage.getItem("kbw-lang") || "zh";
    await this.load(this.lang);
  },
  async load(lang) {
    try {
      const res = await fetch(`/i18n/${lang}.json`);
      this.dict = await res.json();
    } catch (e) {
      this.dict = {};
    }
    this.lang = lang;
    document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
    this.apply();
    document.dispatchEvent(new CustomEvent("i18n:changed", { detail: lang }));
  },
  apply() {
    document.querySelectorAll("[data-i18n]").forEach((el) => {
      const k = el.dataset.i18n;
      if (this.dict[k] != null) el.textContent = this.dict[k];
    });
    document.querySelectorAll("[data-i18n-placeholder]").forEach((el) => {
      const k = el.dataset.i18nPlaceholder;
      if (this.dict[k] != null) el.placeholder = this.dict[k];
    });
    document.querySelectorAll("[data-i18n-title]").forEach((el) => {
      const k = el.dataset.i18nTitle;
      if (this.dict[k] != null) el.title = this.dict[k];
    });
    document.title = this.t("app.name") + (this.lang === "zh" ? " · 我的工作台" : "");
    document.querySelectorAll(".lang-btn").forEach((b) =>
      b.classList.toggle("is-active", b.dataset.lang === this.lang)
    );
  },
  t(key) {
    return (this.dict && this.dict[key]) || key;
  },
};
