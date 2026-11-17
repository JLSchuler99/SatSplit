import torch
from transformers import AutoModelForSeq2SeqLM, AutoTokenizer
from fastapi import FastAPI
from pydantic import BaseModel

MODEL_NAME = "facebook/nllb-200-distilled-600M"

# Map Tesseract language codes to NLLB language codes
LANG_MAP = {
    "spa": "spa_Latn",
    "fra": "fra_Latn",
    "deu": "deu_Latn",
    "ita": "ita_Latn",
    "por": "por_Latn",
    "nld": "nld_Latn",
    "dan": "dan_Latn",
    "nor": "nor_Latn",
    "swe": "swe_Latn",
    "fin": "fin_Latn",
    "pol": "pol_Latn",
    "ces": "ces_Latn",
    "ron": "ron_Latn",
    "hun": "hun_Latn",
    "tur": "tur_Latn",
    "vie": "vie_Latn",
    "ind": "ind_Latn",
    "msa": "zsm_Latn",
    "tgl": "tgl_Latn",
    "rus": "rus_Cyrl",
    "ukr": "ukr_Cyrl",
    "bul": "bul_Cyrl",
    "srp": "srp_Cyrl",
    "ara": "arb_Arab",
    "hin": "hin_Deva",
    "tha": "tha_Thai",
    "jpn": "jpn_Jpan",
    "kor": "kor_Hang",
    "chi_sim": "zho_Hans",
    "chi_tra": "zho_Hant",
}

app = FastAPI()
model = None
tokenizer = None
eng_token_id = None


@app.on_event("startup")
def load_model():
    global model, tokenizer, eng_token_id
    tokenizer = AutoTokenizer.from_pretrained(MODEL_NAME)
    model = AutoModelForSeq2SeqLM.from_pretrained(MODEL_NAME)
    model.eval()
    eng_token_id = tokenizer.convert_tokens_to_ids("eng_Latn")


class TranslateRequest(BaseModel):
    texts: list[str]
    source_lang: str = ""


class TranslateResponse(BaseModel):
    translations: list[str]


@app.post("/translate", response_model=TranslateResponse)
def translate(req: TranslateRequest):
    if not req.texts:
        return TranslateResponse(translations=[])

    src_lang = LANG_MAP.get(req.source_lang, "spa_Latn")
    tokenizer.src_lang = src_lang

    lowered = [t.lower() for t in req.texts]
    batch = tokenizer(lowered, return_tensors="pt", padding=True, truncation=True)

    with torch.no_grad():
        output = model.generate(
            **batch,
            forced_bos_token_id=eng_token_id,
            max_new_tokens=64,
            num_beams=4,
            no_repeat_ngram_size=3,
        )

    translations = tokenizer.batch_decode(output, skip_special_tokens=True)
    translations = [t.capitalize() for t in translations]
    return TranslateResponse(translations=translations)


@app.get("/health")
def health():
    return {"status": "ok"}
