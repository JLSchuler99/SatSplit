// storage wraps localStorage. Some browsers refuse storage access outright
// (blocked cookies, some private modes and in-app browsers) and throw on
// every call. The page must still work then, so values fall back to memory
// and last until the page is closed.
const storage = (() => {
  const memory = new Map();
  return {
    get(key) {
      try {
        const value = window.localStorage.getItem(key);
        if (value !== null) return value;
      } catch (err) {
        // Fall through to memory.
      }
      return memory.has(key) ? memory.get(key) : null;
    },
    set(key, value) {
      memory.set(key, String(value));
      try {
        window.localStorage.setItem(key, value);
      } catch (err) {
        // Kept in memory only.
      }
    },
    remove(key) {
      memory.delete(key);
      try {
        window.localStorage.removeItem(key);
      } catch (err) {
        // Nothing stored to remove.
      }
    },
  };
})();

const viewUpload = document.getElementById("view-upload");
const viewCrop = document.getElementById("view-crop");
const viewReview = document.getElementById("view-review");
const viewShare = document.getElementById("view-share");
const viewJoin = document.getElementById("view-join");
const viewPay = document.getElementById("view-pay");
const viewHistory = document.getElementById("view-history");

const uploadForm = document.getElementById("upload-form");
const uploadResult = document.getElementById("upload-result");
const lnInput = uploadForm ? uploadForm.querySelector("input[name=lightning_address]") : null;
const currencySelect = uploadForm ? uploadForm.querySelector("select[name=currency_code]") : null;
const ocrSelect = uploadForm ? uploadForm.querySelector("select[name=ocr_lang]") : null;

// The OCR language only matters when Tesseract reads the receipt, so the
// field stays hidden unless the server reports that no vision model is set.
const ocrLangField = document.getElementById("ocr-lang-field");
if (ocrLangField) {
  fetch("/api/config")
    .then((response) => (response.ok ? response.json() : null))
    .then((config) => {
      if (config && config.vision === false) ocrLangField.classList.remove("hidden");
    })
    .catch(() => {});
}
const receiptInput = uploadForm ? uploadForm.querySelector("input[name=receipt]") : null;
let chosenReceiptFile = null;

const cropper = document.getElementById("cropper");
const cropImage = document.getElementById("crop-image");
const cropOverlay = document.getElementById("crop-overlay");
const cropSkip = document.getElementById("crop-skip");
const cropApply = document.getElementById("crop-apply");
const cropBack = document.getElementById("crop-back");
const cropError = document.getElementById("crop-error");

const reviewImage = document.getElementById("review-image");
const reviewImageFull = document.getElementById("review-image-full");
const reviewOCR = document.getElementById("review-ocr");
const reviewOCRWrap = document.getElementById("review-ocr-wrap");
const reviewPanel = document.getElementById("review-panel");
const reviewPhotoToggle = document.getElementById("review-photo-toggle");
const reviewCount = document.getElementById("review-count");
const editList = document.getElementById("edit-list");
const addItemButton = document.getElementById("add-item");
const saveItemsButton = document.getElementById("save-items");
const editResult = document.getElementById("edit-result");
const tipPercentInput = document.getElementById("tip-percent");
const taxAmountInput = document.getElementById("tax-amount");
const tipChips = document.getElementById("tip-chips");
const tipOther = document.getElementById("tip-other");

const sharePanel = document.getElementById("share-panel");
const newUploadButton = document.getElementById("new-upload");
const livePill = document.getElementById("live-pill");

const selectionForm = document.getElementById("selection-form");
const selectionResult = document.getElementById("selection-result");
const selectionError = document.getElementById("selection-error");
const payBack = document.getElementById("pay-back");
const itemsContainer = document.getElementById("items");
const subtotalEl = document.getElementById("subtotal");
const receiptSummary = document.getElementById("receipt-summary");

let currentToken = "";
// The token guests use. The host keeps a separate token for editing; the
// share link and QR code must carry this one, never the host's.
let currentGuestToken = "";
let currentCurrency = "USD";
let currentTaxCents = 0;
let currentBillCents = 0;
let taxDirty = false;
let editDirty = false;
let currentItemIdentity = "";
let pendingUpload = null;
let cropRect = null;
let selectionsInterval = null;
let shareItems = [];
let updatePollInterval = null;
// payState follows the invoice on the pay view. invoiceSelectionId is the
// stored row of that invoice, which is not the person's selection_id when it
// is a second invoice.
let payState = null;
let editRowSeq = 0;

const viewNames = new Map([
  [viewUpload, "upload"],
  [viewCrop, "crop"],
  [viewReview, "review"],
  [viewShare, "share"],
  [viewJoin, "join"],
  [viewPay, "pay"],
  [viewHistory, "history"],
]);

const token = window.location.pathname.startsWith("/r/")
  ? window.location.pathname.split("/r/")[1].replace(/\/$/, "")
  : "";

const hostToken = window.location.pathname.startsWith("/h/")
  ? window.location.pathname.split("/h/")[1].replace(/\/$/, "")
  : "";

const isHistoryPage = window.location.pathname === "/history";
// Declared here, before the first view is chosen, because that code calls
// the history functions further down.
const HOST_HISTORY_KEY = "satsplit_host_history";
const HOST_HISTORY_MAX = 100;
let historyLoading = false;

setupThemeToggle();
setupTipChips();
setupHistory();

if (isHistoryPage) {
  setView(viewHistory);
  loadHistory();
} else if (hostToken) {
  storage.set("satsplit_host_token", hostToken);
  restoreHostSession(hostToken);
} else if (token) {
  setView(viewJoin);
  loadReceipt(token);
} else {
  const savedToken = storage.get("satsplit_host_token");
  if (savedToken) {
    restoreHostSession(savedToken);
  } else {
    setView(viewUpload);
    if (lnInput) {
      const cached = storage.get("satsplit_ln_address");
      if (cached) {
        lnInput.value = cached;
      }
    }
  }
}

// ---------- Theme ----------

function setupThemeToggle() {
  const button = document.getElementById("theme-toggle");
  if (!button) return;
  const label = () => {
    const dark = document.documentElement.dataset.theme === "dark";
    button.setAttribute("aria-label", dark ? "Switch to light theme" : "Switch to dark theme");
  };
  label();
  button.addEventListener("click", () => {
    const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    try {
      storage.set("satsplit-theme", next);
    } catch (err) {
      // The choice still applies to this page.
    }
    label();
  });
}

// ---------- Upload ----------

if (uploadForm) {
  // Two inputs feed one choice: the camera, or a photo already on the device.
  // Whichever was used last wins.
  const cameraInput = document.getElementById("receipt-camera");
  const chosenLabel = document.getElementById("receipt-chosen");
  [cameraInput, receiptInput].forEach((input) => {
    if (!input) return;
    input.addEventListener("change", () => {
      if (!input.files || input.files.length === 0) return;
      chosenReceiptFile = input.files[0];
      if (chosenLabel) {
        chosenLabel.textContent = input === cameraInput ? "Photo taken" : chosenReceiptFile.name || "Photo selected";
        chosenLabel.classList.add("chosen");
      }
      clearResult(uploadResult);
    });
  });

  uploadForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    clearResult(uploadResult);
    clearResult(editResult);
    clearResult(cropError);
    if (!chosenReceiptFile) {
      showError(uploadResult, "Take a photo of the receipt or choose one from your gallery.");
      return;
    }
    if (lnInput && lnInput.value.trim() !== "") {
      storage.set("satsplit_ln_address", lnInput.value.trim());
    }

    pendingUpload = {
      file: chosenReceiptFile,
      lightning: lnInput ? lnInput.value.trim() : "",
      currency: currencySelect ? currencySelect.value : "USD",
      ocrLang: ocrSelect ? ocrSelect.value : "",
    };

    const reader = new FileReader();
    reader.onload = () => {
      if (cropImage) {
        cropRect = null;
        if (cropOverlay) cropOverlay.classList.add("hidden");
        cropImage.src = reader.result;
      }
      setView(viewCrop);
    };
    reader.onerror = () => {
      showError(uploadResult, "Your browser could not open that photo. Choose it again, or try a different photo.");
    };
    reader.readAsDataURL(pendingUpload.file);
  });
}

// ---------- Crop ----------

// The crop box starts around the whole photo. Dragging a corner moves that
// corner; dragging anywhere else draws a new box.
if (cropper) {
  let mode = null; // "draw" or a corner name
  let startX = 0;
  let startY = 0;

  const imageRect = () => {
    if (!cropImage || !cropImage.src) return null;
    const rect = cropImage.getBoundingClientRect();
    if (rect.width === 0 || rect.height === 0) return null;
    return rect;
  };

  const pointIn = (event, rect) => ({
    x: Math.max(0, Math.min(event.clientX - rect.left, rect.width)),
    y: Math.max(0, Math.min(event.clientY - rect.top, rect.height)),
  });

  cropper.addEventListener("pointerdown", (event) => {
    const rect = imageRect();
    if (!rect) return;
    const handle = event.target.closest(".crop-handle");
    cropper.setPointerCapture(event.pointerId);
    if (handle && cropRect) {
      mode = handle.dataset.corner;
      // Anchor on the opposite corner so the dragged one follows the finger.
      startX = mode.endsWith("w") ? cropRect.x + cropRect.w : cropRect.x;
      startY = mode.startsWith("n") ? cropRect.y + cropRect.h : cropRect.y;
      return;
    }
    mode = "draw";
    const p = pointIn(event, rect);
    startX = p.x;
    startY = p.y;
  });

  cropper.addEventListener("pointermove", (event) => {
    if (!mode) return;
    const rect = imageRect();
    if (!rect) return;
    const p = pointIn(event, rect);
    setCropRect({
      x: Math.min(startX, p.x),
      y: Math.min(startY, p.y),
      w: Math.abs(p.x - startX),
      h: Math.abs(p.y - startY),
    });
  });

  const stop = (event) => {
    mode = null;
    if (cropper.hasPointerCapture(event.pointerId)) cropper.releasePointerCapture(event.pointerId);
  };
  cropper.addEventListener("pointerup", stop);
  cropper.addEventListener("pointercancel", stop);
}

function setCropRect(rect) {
  cropRect = rect;
  if (!cropOverlay || !cropImage) return;
  const offsetX = cropImage.offsetLeft;
  const offsetY = cropImage.offsetTop;
  cropOverlay.classList.remove("hidden");
  cropOverlay.style.left = `${rect.x + offsetX}px`;
  cropOverlay.style.top = `${rect.y + offsetY}px`;
  cropOverlay.style.width = `${rect.w}px`;
  cropOverlay.style.height = `${rect.h}px`;
}

if (cropImage) {
  cropImage.setAttribute("draggable", "false");
  cropImage.addEventListener("dragstart", (event) => {
    event.preventDefault();
  });
  cropImage.addEventListener("load", () => {
    requestAnimationFrame(() => {
      const rect = cropImage.getBoundingClientRect();
      if (rect.width === 0 || rect.height === 0) return;
      setCropRect({ x: 0, y: 0, w: rect.width, h: rect.height });
    });
  });
}

if (cropBack) {
  cropBack.addEventListener("click", () => {
    if (uploadBusyTimer) return;
    clearResult(cropError);
    // The chosen photo and the form values stay as they were.
    setView(viewUpload);
  });
}

if (cropSkip) {
  cropSkip.addEventListener("click", () => {
    if (!pendingUpload) {
      setView(viewUpload);
      return;
    }
    submitUpload(pendingUpload.file);
  });
}

if (cropApply) {
  cropApply.addEventListener("click", async () => {
    if (!pendingUpload) {
      setView(viewUpload);
      return;
    }
    const cropped = await getCroppedBlob();
    if (!cropped) {
      showError(cropError, "Drag the corners to select the receipt, or skip the crop.");
      return;
    }
    submitUpload(cropped, pendingUpload.file.name);
  });
}

// ---------- Guest selection ----------

if (selectionForm) {
  const participantInput = selectionForm.querySelector("input[name=participant]");
  const cachedName = storage.get("satsplit_participant_name");
  if (cachedName && participantInput) {
    participantInput.value = cachedName;
  }

  selectionForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    clearResult(selectionError);
    const participant = participantInput ? participantInput.value.trim() : "";
    if (!participant) {
      showError(selectionError, "Enter your name first.");
      if (participantInput) participantInput.focus();
      return;
    }
    storage.set("satsplit_participant_name", participant);
    const selected = Array.from(itemsContainer.querySelectorAll("input[type=checkbox]:checked"));
    if (selected.length === 0) {
      showError(selectionError, "Select at least one item.");
      return;
    }
    const itemIds = selected.map((input) => Number(input.value));
    const quantities = {};
    selected.forEach((input) => {
      if (itemQuantity(input) > 1) {
        quantities[input.value] = selectedUnits(input);
      }
    });
    const storageKey = `satsplit_selection_${token}`;
    const prev = JSON.parse(storage.get(storageKey) || "null");
    const body = { participant, item_ids: itemIds, quantities };
    if (prev && prev.selection_id) {
      body.replace_selection_id = prev.selection_id;
      if (prev.edit_secret) body.edit_secret = prev.edit_secret;
    }
    const submitButton = selectionForm.querySelector("button[type=submit]");
    if (submitButton) submitButton.disabled = true;
    try {
      const response = await fetch(`/api/receipt/${token}/selection`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const payload = await response.json();
      if (!response.ok) {
        showError(selectionError, payload.error || "Invoice failed");
        return;
      }
      storage.set(storageKey, JSON.stringify({
        selection_id: payload.selection_id,
        edit_secret: payload.edit_secret || "",
        item_ids: itemIds,
        quantities,
      }));
      renderSelectionResult(payload);
      setView(viewPay);
      fetchGuestSelections(token);
    } catch (err) {
      showError(selectionError, "Invoice failed");
    } finally {
      if (submitButton) submitButton.disabled = false;
    }
  });
}

if (payBack) {
  payBack.addEventListener("click", backToItems);
}

function backToItems() {
  setView(viewJoin);
}

// ---------- Host review ----------

if (editList) {
  editList.addEventListener("input", () => {
    editDirty = true;
    updateReviewTotals();
  });
}

if (taxAmountInput) {
  taxAmountInput.addEventListener("input", () => {
    taxDirty = true;
    currentTaxCents = readTaxCents();
    updateReviewTotals();
  });
}

if (tipPercentInput) {
  tipPercentInput.addEventListener("input", updateReviewTotals);
}

if (reviewPhotoToggle && reviewPanel) {
  reviewPhotoToggle.addEventListener("click", () => {
    const open = reviewPanel.classList.toggle("hidden") === false;
    reviewPhotoToggle.setAttribute("aria-expanded", String(open));
    reviewPhotoToggle.textContent = open ? "Hide photo" : "View photo";
  });
}

if (addItemButton) {
  addItemButton.addEventListener("click", () => {
    editDirty = true;
    addEditRow({ label: "", amount_cents: 0, split_count: 1, quantity: 1 });
  });
}

if (saveItemsButton) {
  saveItemsButton.addEventListener("click", async () => {
    clearResult(editResult);
    const items = readEditItems();
    if (items === null) {
      return;
    }
    if (items.length === 0) {
      showError(editResult, "Add at least one item.");
      return;
    }
    if (!currentToken) {
      showError(editResult, "Missing receipt token.");
      return;
    }
    try {
      const response = await fetch(`/api/receipt/${currentToken}/items`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          items,
          tip_percent: readTipPercent(),
          tax_cents: readTaxCents(),
        }),
      });
      const payload = await response.json();
      if (!response.ok) {
        showError(editResult, payload.error || "Save failed");
        return;
      }
      setTaxAmount(payload.tax_cents);
      renderEditList(payload.items || items);
      updateSharePanel(payload.items || items);
      setView(viewShare);
      startUpdatePolling();
    } catch (err) {
      showError(editResult, "Save failed");
    }
  });
}

if (newUploadButton) {
  newUploadButton.addEventListener("click", () => {
    storage.remove("satsplit_host_token");
    window.location.href = "/";
  });
}

// setUploadBusy shows that the upload is being worked on. Reading the receipt
// takes a while, and without this the page looks frozen. It also blocks a
// second tap, which would upload and read the receipt twice.
let uploadBusyTimer = null;
function setUploadBusy(busy) {
  const progress = document.getElementById("crop-progress");
  const text = document.getElementById("crop-progress-text");
  ["crop-skip", "crop-apply", "crop-back"].forEach((id) => {
    const button = document.getElementById(id);
    if (button) button.disabled = busy;
  });
  if (uploadBusyTimer) {
    clearInterval(uploadBusyTimer);
    uploadBusyTimer = null;
  }
  if (!progress) return;
  progress.classList.toggle("hidden", !busy);
  if (!busy || !text) return;
  const started = Date.now();
  const tick = () => {
    const seconds = Math.floor((Date.now() - started) / 1000);
    if (seconds < 2) {
      text.textContent = "Uploading…";
    } else if (seconds < 40) {
      text.textContent = `Reading items and checking they add up… ${seconds}s`;
    } else {
      text.textContent = `Still reading… ${seconds}s. Long receipts take a little longer.`;
    }
  };
  tick();
  uploadBusyTimer = setInterval(tick, 1000);
}

async function submitUpload(file, filenameOverride) {
  if (!pendingUpload || uploadBusyTimer) return;
  const formData = new FormData();
  formData.append("lightning_address", pendingUpload.lightning);
  formData.append("currency_code", pendingUpload.currency || "USD");
  formData.append("ocr_lang", pendingUpload.ocrLang || "");
  const filename = filenameOverride || pendingUpload.file.name || "receipt.jpg";
  formData.append("receipt", file, filename);

  clearResult(cropError);
  setUploadBusy(true);
  try {
    const response = await fetch("/api/payer/receipt", {
      method: "POST",
      body: formData,
    });
    const payload = await response.json();
    if (!response.ok) {
      showError(cropError, payload.error || "Upload failed");
      return;
    }
    renderUploadResult(payload);
    setView(viewReview);
  } catch (err) {
    showError(cropError, "Upload failed");
  } finally {
    setUploadBusy(false);
  }
}

async function getCroppedBlob() {
  if (!cropRect || !cropImage || cropRect.w < 10 || cropRect.h < 10) {
    return null;
  }
  const rect = cropImage.getBoundingClientRect();
  const scaleX = cropImage.naturalWidth / rect.width;
  const scaleY = cropImage.naturalHeight / rect.height;
  const sx = Math.max(0, cropRect.x * scaleX);
  const sy = Math.max(0, cropRect.y * scaleY);
  const sw = Math.max(1, cropRect.w * scaleX);
  const sh = Math.max(1, cropRect.h * scaleY);

  const canvas = document.createElement("canvas");
  canvas.width = Math.round(sw);
  canvas.height = Math.round(sh);
  const ctx = canvas.getContext("2d");
  ctx.drawImage(cropImage, sx, sy, sw, sh, 0, 0, canvas.width, canvas.height);

  const type = pendingUpload && pendingUpload.file ? pendingUpload.file.type : "image/jpeg";
  return new Promise((resolve) => {
    canvas.toBlob((blob) => resolve(blob), type || "image/jpeg", 0.9);
  });
}

// showReceipt fills the review screen from a receipt payload.
function showReceipt(payload) {
  const items = payload.items || [];
  if (payload.image_url) {
    if (reviewImage) reviewImage.src = payload.image_url;
    if (reviewImageFull) reviewImageFull.src = payload.image_url;
  }
  if (reviewOCR) {
    reviewOCR.textContent = payload.ocr_text || "";
  }
  if (reviewOCRWrap) {
    reviewOCRWrap.classList.toggle("hidden", !payload.ocr_text);
  }
  if (tipPercentInput) {
    tipPercentInput.value = payload.tip_percent || 0;
    syncTipChips();
  }
  setTaxAmount(payload.tax_cents);
  renderEditList(items);
  currentItemIdentity = itemIdentity(items);
  setReviewStatus(payload);
  currentBillCents = billCentsOf(items);
}

async function restoreHostSession(savedToken) {
  try {
    const response = await fetch(`/api/receipt/${savedToken}`);
    if (!response.ok) {
      storage.remove("satsplit_host_token");
      setView(viewUpload);
      return;
    }
    const payload = await response.json();
    if (payload.is_host === false) {
      // A guest link opened as a host page: it cannot edit, so show the
      // guest view and stop treating it as a host session.
      if (storage.get("satsplit_host_token") === savedToken) {
        storage.remove("satsplit_host_token");
      }
      forgetHostReceipt(savedToken);
      window.location.replace(`/r/${savedToken}`);
      return;
    }
    currentToken = savedToken;
    currentGuestToken = payload.guest_token || savedToken;
    rememberHostReceipt(savedToken);
    currentCurrency = payload.currency_code || "USD";
    const items = payload.items || [];
    showReceipt(payload);

    if (items.length > 0) {
      updateSharePanel(items);
      setView(viewShare);
      startUpdatePolling();
    } else {
      setView(viewReview);
    }
  } catch (err) {
    storage.remove("satsplit_host_token");
    setView(viewUpload);
  }
}

async function loadReceipt(receiptToken) {
  try {
    const response = await fetch(`/api/receipt/${receiptToken}`);
    const payload = await response.json();
    if (!response.ok) {
      showError(selectionError, payload.error || "Receipt not found");
      return;
    }
    currentCurrency = payload.currency_code || "USD";
    setTaxAmount(payload.tax_cents);
    if (tipPercentInput) {
      tipPercentInput.value = payload.tip_percent || 0;
    }
    currentBillCents = billCentsOf(payload.items || []);
    receiptSummary.textContent = `Receipt ${payload.receipt_filename} · ${payload.items.length} items`;
    renderItems(payload.items || []);
    startGuestPolling(receiptToken);
  } catch (err) {
    showError(selectionError, "Receipt not found");
  }
}

// ---------- Guest polling and claim status ----------

let guestPollInterval = null;

function startGuestPolling(receiptToken) {
  if (guestPollInterval) clearInterval(guestPollInterval);
  fetchGuestSelections(receiptToken);
  guestPollInterval = setInterval(() => fetchGuestSelections(receiptToken), 5000);
}

async function fetchGuestSelections(receiptToken) {
  try {
    const response = await fetch(`/api/receipt/${receiptToken}/selections`);
    if (!response.ok) return;
    const payload = await response.json();
    const selections = payload.selections || [];
    applyPaidStatus(selections);
    updatePayStatus(selections);
  } catch (err) {
    // silently ignore polling errors
  }
}

// nameList turns claims into "Ana ×2, Ben" in the order people claimed.
function nameList(claims) {
  const counts = new Map();
  claims.forEach((c) => {
    const name = c.mine ? "You" : c.participant;
    counts.set(name, (counts.get(name) || 0) + (c.quantity || 1));
  });
  return Array.from(counts, ([n, count]) => (count > 1 ? `${n} ×${count}` : n)).join(", ");
}

function setClaimStatus(row, text) {
  const el = row.querySelector(".claim-status");
  if (el) el.textContent = text;
  syncNote(row);
}

function setClaimedOut(row, out) {
  row.classList.toggle("claimed-out", out);
}

function applyPaidStatus(selections) {
  if (!itemsContainer) return;

  // Determine which selection belongs to the current user
  const storageKey = `satsplit_selection_${token}`;
  const myData = JSON.parse(storage.get(storageKey) || "null");
  const mySelectionId = myData ? myData.selection_id : null;
  const myItemIds = myData ? new Set((myData.item_ids || []).map(Number)) : new Set();

  // build item_id -> [{participant, mine}...]
  const claimantsById = {};
  selections.forEach((sel) => {
    const mine = sel.id === mySelectionId;
    (sel.items || []).forEach((ref) => {
      if (!claimantsById[ref.id]) claimantsById[ref.id] = [];
      claimantsById[ref.id].push({ participant: sel.participant, mine, quantity: Number(ref.quantity) || 1 });
    });
  });

  let changed = false;

  // Items with a quantity above one: each person claims a number of units.
  const rows = Array.from(itemsContainer.querySelectorAll(".item-row"));
  rows.forEach((row) => {
    const checkbox = row.querySelector("input[type=checkbox]");
    if (!checkbox || itemQuantity(checkbox) <= 1) return;
    if (applyQuantityStatus(row, checkbox, claimantsById[Number(checkbox.value)] || [])) {
      changed = true;
    }
  });

  // group rows by label for fungible pooling
  const labelGroups = {};
  rows.forEach((row) => {
    const label = row.dataset.label;
    if (!label) return;
    const rowBox = row.querySelector("input[type=checkbox]");
    if (rowBox && itemQuantity(rowBox) > 1) return;
    if (!labelGroups[label]) labelGroups[label] = [];
    labelGroups[label].push(row);
  });

  Object.values(labelGroups).forEach((groupRows) => {
    // pool all claims across all item_ids sharing this label
    const pool = [];
    groupRows.forEach((row) => {
      const checkbox = row.querySelector("input[type=checkbox]");
      if (!checkbox) return;
      const claims = claimantsById[Number(checkbox.value)] || [];
      claims.forEach((c) => pool.push(c));
    });

    let idx = 0;
    groupRows.forEach((row) => {
      const checkbox = row.querySelector("input[type=checkbox]");
      if (!checkbox) return;
      const splitCount = Number(checkbox.dataset.split) || 1;

      // consume slots from the pool
      const taken = [];
      for (let s = 0; s < splitCount && idx < pool.length; s++) {
        taken.push(pool[idx]);
        idx++;
      }
      const filled = taken.length;
      const filledMine = taken.filter((c) => c.mine).length;

      if (filled >= splitCount && filledMine >= splitCount) {
        // fully claimed by me — keep enabled + checked
        checkbox.disabled = false;
        checkbox.checked = true;
        setClaimedOut(row, false);
        setClaimStatus(row, "Yours");
      } else if (filled >= splitCount) {
        // fully claimed by others — disable
        if (!checkbox.disabled) {
          checkbox.disabled = true;
          if (checkbox.checked) {
            checkbox.checked = false;
            changed = true;
          }
        }
        setClaimedOut(row, true);
        setClaimStatus(row, `Claimed by ${nameList(taken)}`);
      } else if (filled > 0 && splitCount > 1) {
        // partially claimed split item — keep enabled
        checkbox.disabled = false;
        setClaimedOut(row, false);
        if (filledMine > 0) {
          checkbox.checked = true;
        }
        const left = splitCount - filled;
        setClaimStatus(row, `${left} ${left === 1 ? "share" : "shares"} left`);
      } else {
        // unclaimed
        checkbox.disabled = false;
        setClaimedOut(row, false);
        setClaimStatus(row, "");
      }
      syncCard(row);
    });
  });

  // Restore checked state from localStorage for own items
  if (mySelectionId) {
    rows.forEach((row) => {
      const checkbox = row.querySelector("input[type=checkbox]");
      if (!checkbox || checkbox.disabled) return;
      if (itemQuantity(checkbox) > 1) return;
      if (myItemIds.has(Number(checkbox.value)) && !checkbox.checked) {
        checkbox.checked = true;
        changed = true;
        syncCard(row);
      }
    });
  }

  if (changed) updateSubtotal();
}

function renderItems(items) {
  // A redraw (for example after changing a split) must not drop what the
  // person has already picked, so remember it by item id.
  const previous = {};
  itemsContainer.querySelectorAll(".item-row").forEach((oldRow) => {
    const box = oldRow.querySelector("input[type=checkbox]");
    if (!box) return;
    previous[box.value] = {
      checked: box.checked,
      units: box.dataset.units,
      touched: oldRow.dataset.touched,
      restored: oldRow.dataset.restored,
    };
  });
  itemsContainer.textContent = "";
  items.forEach((item) => {
    const row = document.createElement("div");
    row.className = "item-row claim-card";
    row.dataset.label = item.label || "Item";

    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.className = "visually-hidden";
    checkbox.value = item.id;
    checkbox.dataset.amount = item.amount_cents || 0;
    checkbox.dataset.split = item.split_count || 1;
    const quantity = Number(item.quantity) || 1;
    checkbox.dataset.qty = quantity;
    checkbox.dataset.units = 1;
    checkbox.dataset.maxUnits = quantity;
    const kept = previous[String(item.id)];
    if (kept) {
      checkbox.checked = kept.checked;
      checkbox.dataset.units = Math.min(Number(kept.units) || 1, quantity);
      if (kept.touched) row.dataset.touched = "1";
      if (kept.restored) row.dataset.restored = "1";
    }
    checkbox.addEventListener("change", () => {
      row.dataset.touched = "1";
      syncQuantityRow(row);
      syncCard(row);
      updateSubtotal();
    });

    const main = document.createElement("label");
    main.className = "claim-main";
    main.appendChild(checkbox);
    main.appendChild(checkmarkBox());

    const text = document.createElement("span");
    text.className = "claim-text";
    appendItemNames(text, item);
    const note = document.createElement("span");
    note.className = "claim-note";
    const base = document.createElement("span");
    base.className = "claim-base";
    const splitCount = item.split_count || 1;
    const unitShare = splitShare(unitsCents(item.amount_cents, 1, quantity), splitCount);
    if (quantity > 1 && splitCount > 1) {
      base.textContent = `${formatPlain(unitShare)} per share of one · split ${splitCount} ways`;
    } else if (quantity > 1) {
      base.textContent = `${formatPlain(unitShare)} each`;
    } else if (splitCount > 1) {
      base.textContent = `Split ${splitCount} ways`;
    }
    const status = document.createElement("span");
    status.className = "claim-status";
    note.appendChild(base);
    note.appendChild(status);
    text.appendChild(note);
    main.appendChild(text);

    const price = document.createElement("span");
    price.className = "claim-price";
    main.appendChild(price);
    row.appendChild(main);

    const controls = document.createElement("div");
    controls.className = "claim-controls";

    // Quantity picker: how many of this item the person ordered.
    if (quantity > 1) {
      const qtyWrap = document.createElement("div");
      qtyWrap.className = "claim-qty guest-qty-wrap";
      const prompt = document.createElement("span");
      prompt.textContent = "How many?";
      const stepper = makeStepper({
        className: "stepper-wide",
        valueClass: "guest-qty-value",
        minusClass: "guest-qty-minus",
        plusClass: "guest-qty-plus",
        minusLabel: "Fewer",
        plusLabel: "More",
      });
      const left = document.createElement("span");
      left.className = "claim-left";
      stepper.minus.addEventListener("click", () => {
        if (checkbox.disabled || !checkbox.checked) return;
        const units = selectedUnits(checkbox);
        if (units > 1) {
          checkbox.dataset.units = units - 1;
        } else {
          checkbox.checked = false;
        }
        row.dataset.touched = "1";
        syncQuantityRow(row);
        syncCard(row);
        updateSubtotal();
      });
      stepper.plus.addEventListener("click", () => {
        if (checkbox.disabled) return;
        const max = Number(checkbox.dataset.maxUnits) || 1;
        if (!checkbox.checked) {
          checkbox.checked = true;
          checkbox.dataset.units = 1;
        } else {
          checkbox.dataset.units = Math.min(selectedUnits(checkbox) + 1, max);
        }
        row.dataset.touched = "1";
        syncQuantityRow(row);
        syncCard(row);
        updateSubtotal();
      });
      qtyWrap.appendChild(prompt);
      qtyWrap.appendChild(stepper.el);
      qtyWrap.appendChild(left);
      controls.appendChild(qtyWrap);
    }

    // Split: how many people share this item. It changes the item for
    // everyone, so it sits behind a toggle.
    const splitWrap = document.createElement("div");
    splitWrap.className = "claim-split guest-split-wrap";
    const splitToggle = document.createElement("button");
    splitToggle.type = "button";
    splitToggle.className = "pill" + (splitCount > 1 ? " filled" : "");
    splitToggle.textContent = splitCount > 1 ? `Split ${splitCount} ways` : "Split";
        const splitStepper = makeStepper({
      className: "split-stepper hidden",
      valueText: `${splitCount} ways`,
      minusLabel: "Fewer people share this",
      plusLabel: "More people share this",
    });
    splitToggle.addEventListener("click", () => {
      splitStepper.el.classList.remove("hidden");
      splitToggle.classList.add("hidden");
      splitStepper.plus.focus();
    });
    const sendSplitDelta = async (delta, btn) => {
      btn.disabled = true;
      try {
        const response = await fetch(`/api/receipt/${token}/items/${item.id}/split`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ delta }),
        });
        if (response.ok) {
          const payload = await response.json();
          renderItems(payload.items || []);
          fetchGuestSelections(token);
        }
      } catch (err) {
        // silently ignore
      }
      btn.disabled = false;
    };
    splitStepper.minus.disabled = splitCount <= 1;
    splitStepper.minus.addEventListener("click", () => sendSplitDelta(-1, splitStepper.minus));
    splitStepper.plus.addEventListener("click", () => sendSplitDelta(1, splitStepper.plus));
    splitWrap.appendChild(splitStepper.el);
    splitWrap.appendChild(splitToggle);
    controls.appendChild(splitWrap);

    row.appendChild(controls);
    syncNote(row);
    syncQuantityRow(row);
    syncCard(row);
    itemsContainer.appendChild(row);
  });
  updateSubtotal();
}

// syncCard reflects a claim card's checkbox in its look and controls.
function syncCard(row) {
  const checkbox = row.querySelector("input[type=checkbox]");
  if (!checkbox) return;
  row.classList.toggle("selected", checkbox.checked);
  const qty = row.querySelector(".claim-qty");
  if (qty) qty.classList.toggle("hidden", !checkbox.checked);
  const split = row.querySelector(".claim-split");
  if (split) split.classList.toggle("hidden", !checkbox.checked);
  const controls = row.querySelector(".claim-controls");
  if (controls) {
    const anyVisible = Array.from(controls.children).some((el) => !el.classList.contains("hidden"));
    controls.classList.toggle("hidden", !anyVisible);
  }
}

// syncNote joins the note's two parts with a dot when both are present.
function syncNote(row) {
  const note = row.querySelector(".claim-note");
  if (!note) return;
  const base = note.querySelector(".claim-base");
  const status = note.querySelector(".claim-status");
  if (base && status) {
    status.classList.toggle("has-sep", Boolean(base.textContent && status.textContent));
  }
  note.classList.toggle("hidden", !note.textContent);
}

function itemQuantity(checkbox) {
  return Number(checkbox.dataset.qty) || 1;
}

// selectedUnits is how many units of the row's item this person has picked.
function selectedUnits(checkbox) {
  const quantity = itemQuantity(checkbox);
  const units = Number(checkbox.dataset.units) || 1;
  return Math.max(1, Math.min(units, quantity));
}

// unitsCents mirrors the server: the cost of some units of a line total.
function unitsCents(amountCents, units, quantity) {
  if (!quantity || quantity <= 1 || units >= quantity) return amountCents;
  return Math.round((amountCents * units) / quantity);
}

// syncQuantityRow redraws the unit picker of a quantity row.
function syncQuantityRow(row) {
  const checkbox = row.querySelector("input[type=checkbox]");
  const valueEl = row.querySelector(".guest-qty-value");
  if (!checkbox || !valueEl) return;
  const max = Math.max(0, Number(checkbox.dataset.maxUnits) || 0);
  if (checkbox.checked && selectedUnits(checkbox) > max && max > 0) {
    checkbox.dataset.units = max;
  }
  const shown = checkbox.checked ? selectedUnits(checkbox) : 0;
  valueEl.textContent = String(shown);
  const left = row.querySelector(".claim-left");
  if (left) left.textContent = `of ${max} left`;
  const minus = row.querySelector(".guest-qty-minus");
  const plus = row.querySelector(".guest-qty-plus");
  if (minus) minus.disabled = checkbox.disabled || shown === 0;
  if (plus) plus.disabled = checkbox.disabled || shown >= max;
}

// applyQuantityStatus updates a quantity row from everyone's claims. It
// returns true when the row's selection changed.
function applyQuantityStatus(row, checkbox, claims) {
  const quantity = itemQuantity(checkbox);
  // Each unit can be shared split ways, so the row holds quantity * split
  // shares in total and one person can take at most one share of each unit.
  const split = Number(checkbox.dataset.split) || 1;
  const capacity = quantity * split;
  let others = 0;
  let mine = 0;
  claims.forEach((c) => {
    if (c.mine) mine += c.quantity;
    else others += c.quantity;
  });
  const available = Math.max(0, Math.min(quantity, capacity - others));
  checkbox.dataset.maxUnits = available;
  let changed = false;

  if (available <= 0) {
    if (checkbox.checked) {
      checkbox.checked = false;
      changed = true;
    }
    checkbox.disabled = true;
    setClaimedOut(row, true);
    setClaimStatus(row, `Claimed by ${nameList(claims)}`);
  } else {
    checkbox.disabled = false;
    setClaimedOut(row, false);
    // Bring back this person's earlier pick once, then leave their edits alone.
    if (mine > 0 && !row.dataset.touched && !row.dataset.restored) {
      row.dataset.restored = "1";
      const units = Math.min(mine, available);
      if (!checkbox.checked || selectedUnits(checkbox) !== units) {
        checkbox.checked = true;
        checkbox.dataset.units = units;
        changed = true;
      }
    }
    setClaimStatus(row, others + mine > 0 ? nameList(claims) : "");
  }
  syncQuantityRow(row);
  syncCard(row);
  return changed;
}

function updateSubtotal() {
  const rows = Array.from(itemsContainer.querySelectorAll(".item-row"));
  let food = 0;
  rows.forEach((row) => {
    const input = row.querySelector("input[type=checkbox]");
    if (!input) return;
    const amount = Number(input.dataset.amount || 0);
    const split = Number(input.dataset.split || 1);
    const quantity = itemQuantity(input);
    // A picked card shows what this person pays for it; otherwise one unit's share.
    const units = input.checked ? selectedUnits(input) : 1;
    const cost = splitShare(unitsCents(amount, units, quantity), split);
    if (input.checked) food += cost;
    const price = row.querySelector(".claim-price");
    if (price) price.textContent = formatPlain(cost);
  });
  const tax = taxShareCents(currentTaxCents, food, currentBillCents);
  const tipPct = readTipPercent();
  const tip = tipPct > 0 ? Math.round(food * tipPct / 100) : 0;
  if (subtotalEl) subtotalEl.textContent = formatPlain(food);
  const taxRow = document.getElementById("guest-tax-row");
  const taxEl = document.getElementById("guest-tax");
  const tipRow = document.getElementById("guest-tip-row");
  const tipEl = document.getElementById("guest-tip");
  const tipLabel = document.getElementById("guest-tip-label");
  const totalEl = document.getElementById("guest-total");
  if (taxRow && taxEl) {
    taxEl.textContent = formatPlain(tax);
    taxRow.classList.toggle("hidden", currentTaxCents <= 0);
  }
  if (tipRow && tipEl) {
    tipEl.textContent = formatPlain(tip);
    if (tipLabel) tipLabel.textContent = `Tip ${tipPct}%`;
    tipRow.classList.toggle("hidden", tipPct <= 0);
  }
  if (totalEl) totalEl.textContent = formatCents(food + tax + tip);
}

function renderUploadResult(payload) {
  currentToken = payload.token;
  currentGuestToken = payload.guest_token || payload.token;
  currentCurrency = payload.currency_code || "USD";
  storage.set("satsplit_host_token", currentToken);
  rememberHostReceipt(currentToken);
  showReceipt(payload);
  startUpdatePolling();
}

// ---------- Pay ----------

function renderSelectionResult(payload) {
  currentCurrency = payload.currency_code || currentCurrency;
  // Built with DOM calls, not innerHTML: the invoice text comes from the
  // host's Lightning address provider and must never be parsed as HTML.
  selectionResult.textContent = "";
  const alreadyPaid = Number(payload.already_paid_cents) || 0;
  const invoice = String(payload.invoice || "");
  const invoiceCents = Number(payload.invoice_cents) || Number(payload.total_cents) || 0;
  payState = {
    invoiceSelectionId: Number(payload.invoice_selection_id) || Number(payload.selection_id) || 0,
    alreadyPaid: alreadyPaid > 0,
  };

  const hero = document.createElement("div");
  hero.className = "pay-hero";
  const sats = document.createElement("p");
  sats.className = "pay-sats";
  sats.appendChild(document.createTextNode(Math.ceil((Number(payload.amount_msat) || 0) / 1000).toLocaleString()));
  const unit = document.createElement("small");
  unit.textContent = "sats";
  sats.appendChild(unit);
  const fiat = document.createElement("p");
  fiat.className = "pay-fiat";
  fiat.textContent = `≈ ${formatCents(invoiceCents)} at the current rate`;
  hero.appendChild(sats);
  hero.appendChild(fiat);
  selectionResult.appendChild(hero);

  const card = document.createElement("div");
  card.className = "card pay-card";
  if (payState.invoiceSelectionId) {
    const frame = document.createElement("div");
    frame.className = "qr-frame qr-frame-lg";
    const qr = document.createElement("img");
    qr.src = `/api/receipt/${token}/selections/${payState.invoiceSelectionId}/qr`;
    qr.alt = "QR code of the Lightning invoice";
    qr.width = 236;
    qr.height = 236;
    frame.appendChild(qr);
    const badge = document.createElement("span");
    badge.className = "qr-badge";
    badge.appendChild(svgIcon("i-bolt"));
    frame.appendChild(badge);
    card.appendChild(frame);
  }
  const open = document.createElement("a");
  open.className = "btn btn-primary";
  open.href = `lightning:${invoice}`;
  open.textContent = "Open in wallet";
  card.appendChild(open);
  card.appendChild(copyRow(invoice, "Lightning invoice"));
  selectionResult.appendChild(card);

  const status = document.createElement("p");
  status.id = "pay-status";
  status.setAttribute("role", "status");
  selectionResult.appendChild(status);

  const breakdownCard = document.createElement("div");
  breakdownCard.className = "card";
  const dl = document.createElement("dl");
  dl.className = "breakdown breakdown-plain";
  const tipPct = Number(payload.tip_percent) || 0;
  addBreakdownRow(dl, "Your items", formatPlain(payload.subtotal_cents || 0));
  addBreakdownRow(dl, "Tax and service share", formatPlain(payload.tax_cents || 0));
  addBreakdownRow(dl, `Tip ${tipPct}%`, formatPlain(payload.tip_cents || 0));
  if (alreadyPaid > 0) {
    addBreakdownRow(dl, "Total", formatPlain(payload.total_cents || 0));
    addBreakdownRow(dl, "Already paid", `−${formatPlain(alreadyPaid)}`);
    addBreakdownRow(dl, "This invoice", formatCents(invoiceCents), "breakdown-total");
  } else {
    addBreakdownRow(dl, "Total", formatCents(payload.total_cents || 0), "breakdown-total");
  }
  breakdownCard.appendChild(dl);
  selectionResult.appendChild(breakdownCard);

  const footer = document.createElement("p");
  footer.className = "pay-footer";
  footer.appendChild(document.createTextNode("Forgot something? "));
  const change = document.createElement("button");
  change.type = "button";
  change.className = "link-button inline-link";
  change.textContent = "Change your items";
  change.addEventListener("click", backToItems);
  footer.appendChild(change);
  footer.appendChild(document.createTextNode(". "));
  const note = document.createElement("span");
  note.id = "pay-footer-note";
  footer.appendChild(note);
  selectionResult.appendChild(footer);

  wireCopy(selectionResult);
  renderPayStatus(null);
}

// updatePayStatus follows the invoice on the pay view in the guest poll.
function updatePayStatus(selections) {
  if (!payState || !payState.invoiceSelectionId) return;
  const sel = selections.find((s) => s.id === payState.invoiceSelectionId);
  if (!sel) return;
  renderPayStatus(sel);
}

function renderPayStatus(sel) {
  const status = document.getElementById("pay-status");
  const note = document.getElementById("pay-footer-note");
  if (!status) return;
  status.textContent = "";
  let kind = "neutral";
  let text = "Your wallet will confirm the payment.";
  let footer = "";
  // The server bills only the difference once an earlier invoice of this
  // person is known to be paid; before that a change replaces the invoice.
  const difference = "You’ll only be invoiced for the difference.";
  if (sel && sel.paid) {
    kind = "ok";
    text = "Paid. Thanks!";
    footer = difference;
  } else if (sel && sel.verifiable) {
    kind = "wait";
    text = "Waiting for payment…";
    footer = payState && payState.alreadyPaid ? difference : "Changing items now replaces this invoice.";
  } else if (sel) {
    footer = "This creates a new invoice for the full amount, so change items before you pay.";
  }
  status.className = `banner${kind === "neutral" ? "" : ` banner-${kind}`}`;
  if (kind === "ok") {
    status.appendChild(svgIcon("i-check"));
  } else if (kind === "wait") {
    const dot = document.createElement("span");
    dot.className = "pulse-dot";
    dot.setAttribute("aria-hidden", "true");
    status.appendChild(dot);
  }
  const span = document.createElement("span");
  span.textContent = text;
  status.appendChild(span);
  if (note) note.textContent = footer;
}

// ---------- Host share ----------

function updateSharePanel(items) {
  if (!sharePanel) return;
  shareItems = items;
  const shareURL = `${window.location.origin}/r/${currentGuestToken || currentToken}`;

  sharePanel.textContent = "";

  // Share card
  const shareCard = document.createElement("div");
  shareCard.className = "card share-card";
  const heading = document.createElement("h1");
  heading.textContent = "Share with the table";
  shareCard.appendChild(heading);

  const qrDiv = document.createElement("div");
  qrDiv.className = "qr-frame";
  const qrImg = document.createElement("img");
  qrImg.src = `/api/receipt/${currentToken}/qr`;
  qrImg.alt = "QR code of the share link";
  qrImg.width = 200;
  qrImg.height = 200;
  qrDiv.appendChild(qrImg);
  shareCard.appendChild(qrDiv);

  shareCard.appendChild(copyRow(shareURL, "Share link", true));

  if (navigator.share) {
    const shareBtn = document.createElement("button");
    shareBtn.type = "button";
    shareBtn.className = "btn btn-secondary";
    shareBtn.appendChild(svgIcon("i-share"));
    shareBtn.appendChild(document.createTextNode("Share link"));
    shareBtn.addEventListener("click", () => {
      navigator.share({ title: "SatSplit", text: "Pick what you had and pay your share.", url: shareURL }).catch(() => {});
    });
    shareCard.appendChild(shareBtn);
  }

  const hostHint = document.createElement("p");
  hostHint.className = "center-note";
  // The address bar now holds the host link, which can edit the receipt.
  hostHint.textContent = "Bookmark this page to come back as host. Share only the link above.";
  shareCard.appendChild(hostHint);
  sharePanel.appendChild(shareCard);

  if (window.history && window.history.replaceState) {
    window.history.replaceState(null, "", `/h/${currentToken}`);
  }

  // Claimed card
  const receiptTotal = billCentsOf(items);
  currentBillCents = receiptTotal;
  const tipPct = readTipPercent();
  const tipCents = tipPct > 0 ? Math.round(receiptTotal * tipPct / 100) : 0;
  const grandTotal = receiptTotal + currentTaxCents + tipCents;

  const claimed = document.createElement("div");
  claimed.className = "stage-card";
  const top = document.createElement("div");
  top.className = "claimed-top";
  const claimedLabel = document.createElement("span");
  claimedLabel.textContent = "Claimed";
  const ofTotal = document.createElement("span");
  ofTotal.appendChild(document.createTextNode("of "));
  const ofAmount = document.createElement("span");
  ofAmount.className = "mono";
  ofAmount.textContent = formatCents(grandTotal);
  ofTotal.appendChild(ofAmount);
  top.appendChild(claimedLabel);
  top.appendChild(ofTotal);
  claimed.appendChild(top);
  const claimedValue = document.createElement("p");
  claimedValue.id = "share-claimed";
  claimedValue.className = "claimed-amount";
  claimedValue.textContent = formatCents(0);
  claimed.appendChild(claimedValue);
  const bar = document.createElement("div");
  bar.className = "progress";
  bar.setAttribute("role", "progressbar");
  bar.setAttribute("aria-label", "Share of the bill claimed");
  bar.setAttribute("aria-valuemin", "0");
  bar.setAttribute("aria-valuemax", "100");
  bar.id = "share-progress";
  bar.appendChild(document.createElement("span"));
  claimed.appendChild(bar);
  const rest = document.createElement("p");
  rest.className = "claimed-rest";
  const remaining = document.createElement("span");
  remaining.id = "share-remaining";
  remaining.className = "mono";
  remaining.textContent = formatCents(grandTotal);
  rest.appendChild(remaining);
  rest.appendChild(document.createTextNode(" still unclaimed"));
  claimed.appendChild(rest);
  sharePanel.appendChild(claimed);

  // Who's paid
  const whoSection = document.createElement("section");
  whoSection.className = "stack";
  const whoHeading = document.createElement("h2");
  whoHeading.textContent = "Who’s paid";
  whoSection.appendChild(whoHeading);
  const selectionsDetail = document.createElement("div");
  selectionsDetail.id = "share-selections-detail";
  selectionsDetail.className = "who-list";
  const empty = document.createElement("p");
  empty.className = "who-empty";
  empty.textContent = "No one has picked their items yet.";
  selectionsDetail.appendChild(empty);
  whoSection.appendChild(selectionsDetail);
  sharePanel.appendChild(whoSection);

  // Items
  const itemsSection = document.createElement("section");
  itemsSection.className = "stack";
  const itemsHeading = document.createElement("h2");
  itemsHeading.textContent = "Items";
  itemsSection.appendChild(itemsHeading);
  const itemsList = document.createElement("div");
  itemsList.id = "share-items-list";
  itemsList.className = "card list-card rows share-items";
  items.forEach((item) => {
    const quantity = item.quantity || 1;
    const row = document.createElement("div");
    row.className = "share-item-row";
    row.dataset.itemId = item.id;
    row.dataset.splitCount = item.split_count || 1;
    row.dataset.quantity = quantity;
    // Rows with the same name and quantity share one pool of claims.
    row.dataset.group = `${item.label || "Item"}×${quantity}`;

    const label = document.createElement("span");
    label.className = "share-item-label";
    appendItemNames(label, item, quantity > 1 ? ` ×${quantity}` : "");
    row.appendChild(label);

    const count = document.createElement("span");
    count.className = "share-item-count share-item-status";
    row.appendChild(count);

    // The bar and the names share one line while the names fit beside the
    // bar; a long list wraps onto its own line below it.
    const foot = document.createElement("div");
    foot.className = "share-item-foot";

    const progress = document.createElement("div");
    progress.className = "progress progress-thin";
    progress.setAttribute("aria-hidden", "true");
    progress.appendChild(document.createElement("span"));
    foot.appendChild(progress);

    const names = document.createElement("span");
    names.className = "share-item-names";
    foot.appendChild(names);
    row.appendChild(foot);

    itemsList.appendChild(row);
  });
  itemsSection.appendChild(itemsList);
  sharePanel.appendChild(itemsSection);

  const disclaimer = document.createElement("p");
  disclaimer.className = "hint";
  disclaimer.textContent = "Paid status comes from your Lightning address provider. If it can’t report payments, match the payment hash in your wallet.";
  sharePanel.appendChild(disclaimer);

  wireCopy(sharePanel);
  startSelectionsPolling();
}

function startSelectionsPolling() {
  if (selectionsInterval) clearInterval(selectionsInterval);
  fetchSelections();
  selectionsInterval = setInterval(fetchSelections, 5000);
  syncLivePill();
}

async function fetchSelections() {
  if (!currentToken) return;
  try {
    const response = await fetch(`/api/receipt/${currentToken}/selections`);
    if (!response.ok) return;
    const payload = await response.json();
    applySelections(payload.selections || []);
  } catch (err) {
    // silently ignore polling errors
  }
}

function applySelections(selections) {
  const container = document.getElementById("share-items-list");
  if (!container) return;

  // build map: item_id -> [participant, participant, ...]
  const claimantsById = {};
  selections.forEach((sel) => {
    (sel.items || []).forEach((ref) => {
      if (!claimantsById[ref.id]) claimantsById[ref.id] = [];
      const units = Number(ref.quantity) || 1;
      for (let u = 0; u < units; u++) claimantsById[ref.id].push(sel.participant);
    });
  });

  // group rows by label so fungible items (same name) share a pool
  const rows = Array.from(container.querySelectorAll(".share-item-row"));
  const labelGroups = {};
  rows.forEach((row) => {
    const label = row.dataset.group;
    if (!labelGroups[label]) labelGroups[label] = [];
    labelGroups[label].push(row);
  });

  // for each label group, pool all claimants then redistribute across rows
  Object.values(labelGroups).forEach((groupRows) => {
    const pool = [];
    groupRows.forEach((row) => {
      const names = claimantsById[Number(row.dataset.itemId)] || [];
      names.forEach((n) => pool.push(n));
    });

    let idx = 0;
    groupRows.forEach((row) => {
      const countEl = row.querySelector(".share-item-count");
      const namesEl = row.querySelector(".share-item-names");
      const bar = row.querySelector(".progress span");
      const rowQuantity = Number(row.dataset.quantity) || 1;
      const split = Number(row.dataset.splitCount) || 1;
      const slots = rowQuantity * split;
      const assigned = [];
      for (let s = 0; s < slots && idx < pool.length; s++) {
        assigned.push(pool[idx++]);
      }
      const full = assigned.length >= slots;
      row.classList.toggle("claimed", assigned.length > 0 && full);
      row.classList.toggle("partly-claimed", assigned.length > 0 && !full);
      if (countEl) {
        countEl.textContent = `${assigned.length}/${slots}${split > 1 && rowQuantity === 1 ? " shares" : ""}`;
      }
      if (bar) bar.style.width = `${Math.round((Math.min(assigned.length, slots) / slots) * 100)}%`;
      if (namesEl) {
        // One entry per person, in the order they first claimed: "Ana ×2, Ben".
        // The no-break space keeps a count on the same line as its name.
        const counts = new Map();
        assigned.forEach((n) => counts.set(n, (counts.get(n) || 0) + 1));
        namesEl.textContent = Array.from(counts, ([n, c]) => (c > 1 ? `${n} ×${c}` : n)).join(", ");
      }
    });
  });

  // Update claimed/remaining totals
  const claimedCents = selections.reduce((sum, sel) => sum + (sel.total_cents || 0), 0);
  const claimedEl = document.getElementById("share-claimed");
  const remainingEl = document.getElementById("share-remaining");
  if (claimedEl || remainingEl) {
    const receiptTotal = billCentsOf(shareItems);
    const tipPct = readTipPercent();
    const tipCents = tipPct > 0 ? Math.round(receiptTotal * tipPct / 100) : 0;
    const grandTotal = receiptTotal + currentTaxCents + tipCents;
    if (claimedEl) claimedEl.textContent = formatCents(claimedCents);
    if (remainingEl) remainingEl.textContent = formatCents(Math.max(0, grandTotal - claimedCents));
    const progress = document.getElementById("share-progress");
    if (progress) {
      const pct = grandTotal > 0 ? Math.min(100, Math.round((claimedCents / grandTotal) * 100)) : 0;
      progress.setAttribute("aria-valuenow", String(pct));
      const fill = progress.querySelector("span");
      if (fill) fill.style.width = `${pct}%`;
    }
  }

  // Render per-selection detail
  const detailContainer = document.getElementById("share-selections-detail");
  const detailKey = JSON.stringify(selections.map((sel) => [sel.id, sel.total_cents, sel.paid, sel.verifiable, sel.invoice, sel.preimage]));
  if (detailContainer && detailContainer.dataset.key !== detailKey) {
    detailContainer.dataset.key = detailKey;
    detailContainer.textContent = "";
    if (selections.length === 0) {
      const empty = document.createElement("p");
      empty.className = "who-empty";
      empty.textContent = "No one has picked their items yet.";
      detailContainer.appendChild(empty);
    }
    selections.forEach((sel) => {
      detailContainer.appendChild(buildWhoCard(sel));
    });
    wireCopy(detailContainer);
  }
}

// buildWhoCard draws one person's invoice: name, amount, paid status, and the
// payment hash to match by hand when the status is not known. currency is the
// receipt's currency; it defaults to the receipt on screen.
function buildWhoCard(sel, currency) {
  const entry = document.createElement("div");
  entry.className = "card who-card";

  const head = document.createElement("div");
  head.className = "who-head";
  const avatar = document.createElement("span");
  avatar.className = "avatar";
  avatar.setAttribute("aria-hidden", "true");
  avatar.textContent = (Array.from(sel.participant || "?")[0] || "?").toUpperCase();
  head.appendChild(avatar);

  const nameBox = document.createElement("div");
  nameBox.className = "who-name";
  const name = document.createElement("strong");
  name.textContent = sel.parent_id ? `${sel.participant} (added items)` : sel.participant;
  nameBox.appendChild(name);
  if (sel.amount_sats > 0) {
    const sats = document.createElement("span");
    sats.className = "who-sats";
    sats.textContent = `${Number(sel.amount_sats).toLocaleString()} sats`;
    nameBox.appendChild(sats);
  }
  head.appendChild(nameBox);

  const end = document.createElement("div");
  end.className = "who-end";
  const amount = document.createElement("span");
  amount.className = "who-amount";
  amount.textContent = formatPlain(sel.total_cents || 0, currency);
  end.appendChild(amount);

  // Paid status comes from the provider (LUD-21). Without it the host
  // matches the payment hash against their wallet's payment list.
  const badge = document.createElement("span");
  badge.className = "badge selection-status";
  if (sel.paid) {
    badge.classList.add("badge-ok");
    badge.appendChild(svgIcon("i-check"));
    badge.appendChild(document.createTextNode("Paid"));
  } else if (sel.verifiable) {
    badge.classList.add("badge-wait");
    badge.textContent = "Not paid yet";
  } else {
    badge.textContent = "Can’t confirm";
    badge.title = "This Lightning address provider does not report payments. Match the payment hash in your wallet.";
  }
  end.appendChild(badge);
  head.appendChild(end);
  entry.appendChild(head);

  const actions = document.createElement("div");
  actions.className = "who-actions";
  const addCopy = (text, value, mono) => {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "btn btn-small" + (mono ? " mono" : "");
    btn.setAttribute("data-copy", value);
    btn.textContent = text;
    actions.appendChild(btn);
    return btn;
  };
  if (!sel.paid) {
    if (sel.invoice) addCopy("Copy invoice", sel.invoice);
    if (sel.payment_hash) {
      const btn = addCopy(`hash ${sel.payment_hash.slice(0, 8)}…`, sel.payment_hash, true);
      btn.setAttribute("aria-label", `Copy payment hash ${sel.payment_hash}`);
      btn.title = sel.payment_hash;
    }
  }
  if (sel.preimage) addCopy("Copy preimage", sel.preimage);
  if (actions.childNodes.length > 0) entry.appendChild(actions);
  return entry;
}

// ---------- Views ----------

function setView(activeView) {
  if (activeView !== viewShare && selectionsInterval) {
    clearInterval(selectionsInterval);
    selectionsInterval = null;
  }
  if (activeView === viewUpload && updatePollInterval) {
    clearInterval(updatePollInterval);
    updatePollInterval = null;
  }
  const previous = document.body.dataset.view;
  viewNames.forEach((_, view) => {
    if (!view) return;
    if (view === activeView) {
      view.classList.remove("hidden");
    } else {
      view.classList.add("hidden");
    }
  });
  document.body.dataset.view = viewNames.get(activeView) || "";
  syncLivePill();
  if (previous && previous !== document.body.dataset.view) window.scrollTo(0, 0);
}

function syncLivePill() {
  if (!livePill) return;
  livePill.classList.toggle("hidden", !(selectionsInterval && document.body.dataset.view === "share"));
}

// ---------- Small DOM helpers ----------

function svgIcon(id) {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("class", "icon");
  svg.setAttribute("aria-hidden", "true");
  const use = document.createElementNS(ns, "use");
  use.setAttribute("href", `#${id}`);
  svg.appendChild(use);
  return svg;
}

function checkmarkBox() {
  const box = document.createElement("span");
  box.className = "checkmark";
  box.setAttribute("aria-hidden", "true");
  box.appendChild(svgIcon("i-check"));
  return box;
}

// makeStepper builds "− value +". The caller wires the buttons.
function makeStepper(opts) {
  const el = document.createElement("div");
  el.className = `stepper${opts.className ? ` ${opts.className}` : ""}`;
  const minus = document.createElement("button");
  minus.type = "button";
  if (opts.minusClass) minus.className = opts.minusClass;
  minus.setAttribute("aria-label", opts.minusLabel);
  minus.appendChild(svgIcon("i-minus"));
  const value = document.createElement("span");
  value.className = `stepper-value${opts.valueClass ? ` ${opts.valueClass}` : ""}`;
  value.setAttribute("aria-live", "polite");
  if (opts.valueText) value.textContent = opts.valueText;
  const plus = document.createElement("button");
  plus.type = "button";
  if (opts.plusClass) plus.className = opts.plusClass;
  plus.setAttribute("aria-label", opts.plusLabel);
  plus.appendChild(svgIcon("i-plus"));
  el.appendChild(minus);
  el.appendChild(value);
  el.appendChild(plus);
  return { el, minus, value, plus };
}

function copyRow(value, label, primary) {
  const row = document.createElement("div");
  row.className = "copy-row";
  const input = document.createElement("input");
  input.type = "text";
  input.readOnly = true;
  input.value = value;
  input.setAttribute("aria-label", label);
  const button = document.createElement("button");
  button.type = "button";
  button.className = `btn btn-copy ${primary ? "btn-primary" : "btn-secondary"}`;
  button.setAttribute("data-copy", value);
  button.textContent = "Copy";
  row.appendChild(input);
  row.appendChild(button);
  return row;
}

function addBreakdownRow(dl, label, value, className) {
  const row = document.createElement("div");
  if (className) row.className = className;
  const dt = document.createElement("dt");
  dt.textContent = label;
  const dd = document.createElement("dd");
  dd.className = "mono";
  dd.textContent = value;
  row.appendChild(dt);
  row.appendChild(dd);
  dl.appendChild(row);
}

// appendItemNames shows the English name first and the printed name below
// it when a translation differs.
function appendItemNames(parent, item, suffix) {
  const translated = item.translated_label && item.translated_label !== item.label;
  const name = document.createElement("span");
  name.className = "item-name";
  name.textContent = (translated ? item.translated_label : item.label || "Item") + (suffix || "");
  parent.appendChild(name);
  const original = document.createElement("span");
  original.className = "item-original";
  if (translated) {
    original.textContent = item.label;
  } else {
    original.classList.add("hidden");
  }
  parent.appendChild(original);
}

function setItemNames(parent, item, suffix) {
  const name = parent.querySelector(".item-name");
  const original = parent.querySelector(".item-original");
  if (!name || !original) return;
  const translated = item.translated_label && item.translated_label !== item.label;
  const icon = name.querySelector("svg");
  name.textContent = (translated ? item.translated_label : item.label || "Item") + (suffix || "");
  if (icon) name.appendChild(icon);
  original.textContent = translated ? item.label : "";
  original.classList.toggle("hidden", !translated);
}

function wireCopy(scope) {
  scope.querySelectorAll("button[data-copy]").forEach((button) => {
    if (button.dataset.wired) return;
    button.dataset.wired = "1";
    button.addEventListener("click", async () => {
      const value = button.getAttribute("data-copy");
      const original = button.dataset.label || button.textContent;
      button.dataset.label = original;
      try {
        await navigator.clipboard.writeText(value);
        button.textContent = "Copied";
      } catch (err) {
        button.textContent = "Copy failed";
      }
      setTimeout(() => (button.textContent = original), 1200);
    });
  });
}

function showError(el, message) {
  if (!el) return;
  el.classList.remove("hidden");
  el.classList.add("error");
  el.textContent = message;
}

function clearResult(el) {
  if (!el) return;
  el.classList.add("hidden");
  el.classList.remove("error");
  el.textContent = "";
}

// currency defaults to the receipt on screen; the history page passes each
// receipt's own.
function formatCents(cents, currency) {
  const value = (cents / 100);
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: currency || currentCurrency || "USD",
    }).format(value);
  } catch (err) {
    return `$${value.toFixed(2)}`;
  }
}

// formatPlain is formatCents without the currency sign, for list rows.
function formatPlain(cents, currency) {
  const value = (cents / 100);
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: currency || currentCurrency || "USD",
    })
      .formatToParts(value)
      .filter((part) => part.type !== "currency" && part.type !== "literal")
      .map((part) => part.value)
      .join("");
  } catch (err) {
    return value.toFixed(2);
  }
}

// ---------- Review list ----------

function renderEditList(items) {
  if (!editList) return;
  editDirty = false;
  editList.textContent = "";
  if (!items || items.length === 0) {
    addEditRow({ label: "", amount_cents: 0, split_count: 1, quantity: 1 });
  } else {
    items.forEach((item) => addEditRow(item));
  }
  updateReviewCount();
  updateReviewTotals();
}

function addEditRow(item) {
  if (!editList) return;
  const row = document.createElement("div");
  row.className = "edit-row";
  row.dataset.label = item.label || "";
  row.dataset.translated = item.translated_label || "";
  row.dataset.qty = item.quantity || 1;

  const nameItem = () => ({ label: labelInput.value.trim(), translated_label: row.dataset.label === labelInput.value.trim() ? row.dataset.translated : "" });

  // --- Collapsed view ---
  const head = document.createElement("div");
  head.className = "edit-row-head";
  const nameButton = document.createElement("button");
  nameButton.type = "button";
  nameButton.className = "edit-name-button";
  appendItemNames(nameButton, item);
  nameButton.querySelector(".item-name").appendChild(svgIcon("i-pencil"));
  const displayAmount = document.createElement("span");
  displayAmount.className = "item-amount";
  displayAmount.textContent = formatPlain(item.amount_cents || 0);
  head.appendChild(nameButton);
  head.appendChild(displayAmount);

  const controls = document.createElement("div");
  controls.className = "edit-row-controls";

  // Quantity: units on this line; the amount is the total for all of them.
  const qtyStepper = makeStepper({ minusLabel: "Fewer units", plusLabel: "More units", valueClass: "qty-value" });
  const each = document.createElement("span");
  each.className = "item-each";
  const syncQty = () => {
    const q = Number(row.dataset.qty) || 1;
    qtyStepper.value.textContent = `×${q}`;
    qtyStepper.minus.disabled = q <= 1;
    qtyStepper.plus.disabled = q >= 999;
    const cents = amountCentsOf(amountInput.value);
    each.textContent = `${formatPlain(unitsCents(cents, 1, q))} each`;
  };
  qtyStepper.minus.addEventListener("click", () => {
    row.dataset.qty = Math.max(1, (Number(row.dataset.qty) || 1) - 1);
    editDirty = true;
    syncQty();
  });
  qtyStepper.plus.addEventListener("click", () => {
    row.dataset.qty = Math.min(999, (Number(row.dataset.qty) || 1) + 1);
    editDirty = true;
    syncQty();
  });

  const spacer = document.createElement("span");
  spacer.className = "spacer";

  // Split: how many people share the item.
  const splitValue = item.split_count || 1;
  const splitToggle = document.createElement("button");
  splitToggle.type = "button";
  splitToggle.className = "pill";
    const splitStepper = makeStepper({
    className: "split-stepper hidden",
    valueClass: "split-value",
    minusLabel: "Fewer people share this",
    plusLabel: "More people share this",
  });
  splitStepper.value.dataset.split = splitValue;
  const syncSplit = () => {
    const n = Number(splitStepper.value.dataset.split) || 1;
    splitStepper.value.textContent = `${n} ${n === 1 ? "way" : "ways"}`;
    splitStepper.minus.disabled = n <= 1;
    splitToggle.textContent = n > 1 ? `Split ${n} ways` : "Split";
    splitToggle.classList.toggle("filled", n > 1);
  };
  splitToggle.addEventListener("click", () => {
    splitStepper.el.classList.remove("hidden");
    splitToggle.classList.add("hidden");
    splitStepper.plus.focus();
  });
  splitStepper.minus.addEventListener("click", () => {
    editDirty = true;
    splitStepper.value.dataset.split = Math.max(1, (Number(splitStepper.value.dataset.split) || 1) - 1);
    syncSplit();
  });
  splitStepper.plus.addEventListener("click", () => {
    editDirty = true;
    splitStepper.value.dataset.split = (Number(splitStepper.value.dataset.split) || 1) + 1;
    syncSplit();
  });

  controls.appendChild(qtyStepper.el);
  controls.appendChild(each);
  controls.appendChild(spacer);
  controls.appendChild(splitStepper.el);
  controls.appendChild(splitToggle);

  // --- Edit fields ---
  const fields = document.createElement("div");
  fields.className = "edit-fields";
  const fieldsRow = document.createElement("div");
  fieldsRow.className = "edit-fields-row";

  const labelField = document.createElement("div");
  labelField.className = "field";
  const labelInput = document.createElement("input");
  labelInput.type = "text";
  labelInput.className = "edit-label-input";
  labelInput.placeholder = "Item name";
  labelInput.value = item.label || "";
  labelInput.id = `edit-label-${editRowSeq}`;
  const labelLabel = document.createElement("label");
  labelLabel.htmlFor = labelInput.id;
  labelLabel.textContent = "Name";
  labelField.appendChild(labelLabel);
  labelField.appendChild(labelInput);

  const amountField = document.createElement("div");
  amountField.className = "field";
  const amountInput = document.createElement("input");
  amountInput.type = "text";
  amountInput.className = "edit-amount-input mono";
  amountInput.inputMode = "decimal";
  amountInput.pattern = "[0-9]*\\.?[0-9]*";
  amountInput.placeholder = "0.00";
  amountInput.value = ((item.amount_cents || 0) / 100).toFixed(2);
  amountInput.id = `edit-amount-${editRowSeq}`;
  const amountLabel = document.createElement("label");
  amountLabel.htmlFor = amountInput.id;
  amountLabel.textContent = "Line total";
  amountField.appendChild(amountLabel);
  amountField.appendChild(amountInput);
  editRowSeq++;

  fieldsRow.appendChild(labelField);
  fieldsRow.appendChild(amountField);
  fields.appendChild(fieldsRow);
  const hint = document.createElement("p");
  hint.className = "hint";
  hint.textContent = "As printed on the receipt. The English name updates automatically.";
  fields.appendChild(hint);

  const actions = document.createElement("div");
  actions.className = "edit-actions";
  const removeButton = document.createElement("button");
  removeButton.type = "button";
  removeButton.className = "btn btn-secondary";
  removeButton.appendChild(svgIcon("i-trash"));
  removeButton.appendChild(document.createTextNode("Remove"));
  removeButton.addEventListener("click", () => {
    editDirty = true;
    const next = row.nextElementSibling || addItemButton;
    row.remove();
    updateReviewCount();
    updateReviewTotals();
    if (next) next.focus();
  });
  const doneButton = document.createElement("button");
  doneButton.type = "button";
  doneButton.className = "btn btn-primary";
  doneButton.appendChild(svgIcon("i-check"));
  doneButton.appendChild(document.createTextNode("Done"));
  doneButton.addEventListener("click", () => {
    row.classList.remove("editing");
    setItemNames(nameButton, nameItem());
    displayAmount.textContent = formatPlain(amountCentsOf(amountInput.value));
    syncQty();
    nameButton.setAttribute("aria-label", `Edit ${labelInput.value.trim() || "item"}`);
    nameButton.focus();
  });
  actions.appendChild(removeButton);
  actions.appendChild(doneButton);
  fields.appendChild(actions);

  nameButton.setAttribute("aria-label", `Edit ${item.translated_label || item.label || "item"}`);
  nameButton.addEventListener("click", () => {
    row.classList.add("editing");
    labelInput.focus();
  });
  amountInput.addEventListener("input", syncQty);

  row.appendChild(head);
  row.appendChild(controls);
  row.appendChild(fields);

  syncQty();
  syncSplit();

  // New rows with empty label start expanded
  if (!item.label) {
    row.classList.add("editing");
  }

  editList.appendChild(row);
  if (!item.label && editDirty) labelInput.focus();
  updateReviewCount();
}

function amountCentsOf(text) {
  const value = Number(text);
  return Number.isFinite(value) ? Math.round(value * 100) : 0;
}

function updateReviewCount() {
  if (!reviewCount || !editList) return;
  const n = editList.querySelectorAll(".edit-row").length;
  reviewCount.textContent = `${n} ${n === 1 ? "item" : "items"} · ${currentCurrency}`;
}

function updateReviewTotals() {
  if (!editList) return;
  let bill = 0;
  editList.querySelectorAll(".edit-row").forEach((row) => {
    const input = row.querySelector(".edit-amount-input");
    if (input) bill += amountCentsOf(input.value);
  });
  const tax = readTaxCents();
  const tipPct = readTipPercent();
  const tip = tipPct > 0 ? Math.round(bill * tipPct / 100) : 0;
  const set = (id, text) => {
    const el = document.getElementById(id);
    if (el) el.textContent = text;
  };
  set("review-subtotal", formatPlain(bill));
  set("review-tax", formatPlain(tax));
  set("review-tip-label", `Tip ${tipPct}%`);
  set("review-tip", formatPlain(tip));
  set("review-total", formatCents(bill + tax + tip));
}

// ---------- Tip chips ----------

// The chips write to #tip-percent, which stays the source of truth.
function setupTipChips() {
  if (!tipChips || !tipPercentInput) return;
  const chips = Array.from(tipChips.querySelectorAll(".chip"));
  const choose = (chip, focus) => {
    if (chip.dataset.tip === "other") {
      setChecked(chip);
      tipOther.classList.remove("hidden");
      if (focus) tipPercentInput.focus();
    } else {
      tipPercentInput.value = chip.dataset.tip;
      tipOther.classList.add("hidden");
      setChecked(chip);
    }
    updateReviewTotals();
  };
  chips.forEach((chip, i) => {
    chip.addEventListener("click", () => choose(chip, true));
    chip.addEventListener("keydown", (event) => {
      let next = null;
      if (event.key === "ArrowRight" || event.key === "ArrowDown") next = chips[(i + 1) % chips.length];
      if (event.key === "ArrowLeft" || event.key === "ArrowUp") next = chips[(i - 1 + chips.length) % chips.length];
      if (!next) return;
      event.preventDefault();
      next.focus();
      choose(next, false);
    });
  });
  syncTipChips();
}

function setChecked(active) {
  if (!tipChips) return;
  tipChips.querySelectorAll(".chip").forEach((chip) => {
    const on = chip === active;
    chip.setAttribute("aria-checked", String(on));
    chip.tabIndex = on ? 0 : -1;
  });
}

function syncTipChips() {
  if (!tipChips || !tipPercentInput) return;
  const value = readTipPercent();
  const match = Array.from(tipChips.querySelectorAll(".chip")).find((chip) => chip.dataset.tip !== "other" && Number(chip.dataset.tip) === value);
  const chip = match || tipChips.querySelector('[data-tip="other"]');
  setChecked(chip);
  if (tipOther) tipOther.classList.toggle("hidden", Boolean(match));
}

function itemIdentity(items) {
  return (items || [])
    .map((item) => `${item.id || ""}|${item.label}|${item.amount_cents}|${item.split_count || 1}|${item.quantity || 1}`)
    .join("\n");
}

function setReviewStatus(payload) {
  const el = document.getElementById("review-status");
  if (!el || !payload) return;
  el.textContent = "";
  el.className = "banner";
  if (payload.refining) {
    el.classList.add("banner-wait");
    const dot = document.createElement("span");
    dot.className = "pulse-dot";
    dot.setAttribute("aria-hidden", "true");
    el.appendChild(dot);
    const text = document.createElement("span");
    text.textContent = "Checking the receipt again for a more accurate item list. You can edit now; saving keeps your version.";
    el.appendChild(text);
    return;
  }
  if ((payload.items || []).length === 0) {
    el.textContent = "No items detected. Add them below.";
    return;
  }
  el.classList.add("hidden");
}

function readEditItems() {
  if (!editList) return [];
  const rows = Array.from(editList.querySelectorAll(".edit-row"));
  const items = [];
  for (const row of rows) {
    const labelInput = row.querySelector(".edit-label-input");
    const amountInput = row.querySelector(".edit-amount-input");
    if (!labelInput || !amountInput) continue;
    const label = labelInput.value.trim();
    const amountValue = Number(amountInput.value);
    const stepperValue = row.querySelector(".split-value");
    const splitValue = stepperValue ? Number(stepperValue.dataset.split) : 1;
    if (!label) {
      continue;
    }
    if (Number.isNaN(amountValue)) {
      showError(editResult, "One or more amounts are invalid.");
      return null;
    }
    if (!Number.isFinite(splitValue) || splitValue < 1) {
      showError(editResult, "Split count must be 1 or more.");
      return null;
    }
    const amountCents = Math.round(amountValue * 100);
    let quantity = Math.round(Number(row.dataset.qty));
    if (!Number.isFinite(quantity) || quantity < 1) quantity = 1;
    items.push({ label, amount_cents: amountCents, split_count: Math.round(splitValue), quantity });
  }
  return items;
}

function billCentsOf(items) {
  return (items || []).reduce((sum, item) => sum + (item.amount_cents || 0), 0);
}

function taxShareCents(tax, selected, bill) {
  if (tax <= 0 || selected <= 0 || bill <= 0) return 0;
  return Math.round((tax * selected) / bill);
}

function readTaxCents() {
  if (!taxAmountInput) return currentTaxCents || 0;
  const value = Number(taxAmountInput.value);
  if (!Number.isFinite(value) || value < 0) return 0;
  return Math.round(value * 100);
}

function setTaxAmount(cents) {
  currentTaxCents = Number(cents) || 0;
  if (taxAmountInput) {
    taxAmountInput.value = (currentTaxCents / 100).toFixed(2);
  }
  updateReviewTotals();
}

function readTipPercent() {
  if (!tipPercentInput) return 0;
  const value = Number(tipPercentInput.value);
  if (!Number.isFinite(value) || value < 0) {
    return 0;
  }
  return value;
}

function splitShare(amountCents, splitCount) {
  if (!splitCount || splitCount <= 1) return amountCents;
  return Math.round(amountCents / splitCount);
}

function startUpdatePolling() {
  if (updatePollInterval) clearInterval(updatePollInterval);
  if (!currentToken) return;
  let attempts = 0;
  const maxAttempts = 140;
  updatePollInterval = setInterval(async () => {
    attempts++;
    if (attempts > maxAttempts) {
      clearInterval(updatePollInterval);
      updatePollInterval = null;
      return;
    }
    try {
      const response = await fetch(`/api/receipt/${currentToken}`);
      if (!response.ok) return;
      const payload = await response.json();
      const items = payload.items || [];
      const nextIdentity = itemIdentity(items);
      setReviewStatus(payload);
      if (!taxDirty && Number.isFinite(payload.tax_cents)) {
        setTaxAmount(payload.tax_cents);
      }

      if (nextIdentity !== currentItemIdentity) {
        currentItemIdentity = nextIdentity;
        if (!editDirty) {
          renderEditList(items);
          updateSharePanel(items);
          currentItemIdentity = itemIdentity(items);
        }
      } else {
        applyTranslations(items);
      }

      if (!payload.refining && !payload.pending_translation) {
        clearInterval(updatePollInterval);
        updatePollInterval = null;
      }
    } catch (err) {
      // ignore polling errors
    }
  }, 1500);
}

// applyTranslations shows English names that arrive after the list was drawn.
function applyTranslations(items) {
  if (editList) {
    const rows = Array.from(editList.querySelectorAll(".edit-row"));
    items.forEach((item, i) => {
      if (i >= rows.length) return;
      const row = rows[i];
      // Leave a row alone once its printed name was edited.
      const input = row.querySelector(".edit-label-input");
      if (!input || input.value.trim() !== item.label || row.dataset.label !== item.label) return;
      row.dataset.translated = item.translated_label || "";
      const button = row.querySelector(".edit-name-button");
      if (button) setItemNames(button, item);
    });
  }

  const shareList = document.getElementById("share-items-list");
  if (shareList) {
    const rows = Array.from(shareList.querySelectorAll(".share-item-row"));
    items.forEach((item, i) => {
      if (i >= rows.length) return;
      const label = rows[i].querySelector(".share-item-label");
      const quantity = item.quantity || 1;
      if (label) setItemNames(label, item, quantity > 1 ? ` ×${quantity}` : "");
    });
  }
}

// ---------- Host history ----------
// The receipts this browser uploaded or opened as host. Only the receipt
// codes are kept, in this browser's storage; everything shown is fetched
// fresh, so paid status is current each time the page loads.

function readHostHistory() {
  try {
    const list = JSON.parse(storage.get(HOST_HISTORY_KEY) || "[]");
    return Array.isArray(list) ? list.filter((t) => typeof t === "string" && /^[a-f0-9]{8,64}$/i.test(t)) : [];
  } catch (err) {
    return [];
  }
}

function writeHostHistory(list) {
  try {
    storage.set(HOST_HISTORY_KEY, JSON.stringify(list.slice(0, HOST_HISTORY_MAX)));
  } catch (err) {
    // storage unavailable: the page still works, it just forgets
  }
  syncHistoryLink();
}

function rememberHostReceipt(receiptToken) {
  if (!receiptToken || !/^[a-f0-9]{8,64}$/i.test(receiptToken)) return;
  const list = readHostHistory().filter((t) => t !== receiptToken);
  list.unshift(receiptToken);
  writeHostHistory(list);
}

function forgetHostReceipt(receiptToken) {
  writeHostHistory(readHostHistory().filter((t) => t !== receiptToken));
}

// The link to the history page is shown to hosts only, once there is history.
function syncHistoryLink() {
  const link = document.getElementById("history-link");
  if (!link) return;
  const show = !token && !isHistoryPage && readHostHistory().length > 0;
  link.classList.toggle("hidden", !show);
}

async function fetchHistoryEntry(receiptToken) {
  try {
    const [receiptRes, selectionsRes] = await Promise.all([
      fetch(`/api/receipt/${receiptToken}`),
      fetch(`/api/receipt/${receiptToken}/selections`),
    ]);
    if (receiptRes.status === 404) return { token: receiptToken, missing: true };
    if (!receiptRes.ok) return { token: receiptToken, failed: true };
    const receipt = await receiptRes.json();
    const selections = selectionsRes.ok ? (await selectionsRes.json()).selections || [] : [];
    return { token: receiptToken, receipt, selections };
  } catch (err) {
    return { token: receiptToken, failed: true };
  }
}

function historyBadge(text, kind) {
  const badge = document.createElement("span");
  badge.className = "badge" + (kind ? ` badge-${kind}` : "");
  if (kind === "ok") badge.appendChild(svgIcon("i-check"));
  badge.appendChild(document.createTextNode(text));
  return badge;
}

function buildHistoryCard(entry) {
  const card = document.createElement("div");
  card.className = "card history-card";

  const removeButton = document.createElement("button");
  removeButton.type = "button";
  removeButton.className = "link-button history-remove";
  removeButton.textContent = "Remove from list";
  removeButton.addEventListener("click", () => {
    forgetHostReceipt(entry.token);
    card.remove();
    const list = document.getElementById("history-list");
    if (list && !list.querySelector(".history-card")) showHistoryEmpty(list);
  });

  if (entry.missing || entry.failed) {
    const note = document.createElement("p");
    note.className = "history-note";
    note.textContent = entry.missing
      ? `Receipt ${entry.token} no longer exists.`
      : `Receipt ${entry.token} could not be loaded. Try Refresh.`;
    card.appendChild(note);
    const actions = document.createElement("div");
    actions.className = "history-card-actions";
    actions.appendChild(removeButton);
    card.appendChild(actions);
    return card;
  }

  const { receipt, selections } = entry;
  const currency = receipt.currency_code || "USD";
  const items = receipt.items || [];
  const bill = billCentsOf(items);
  const tipPercent = Number(receipt.tip_percent) || 0;
  const tip = tipPercent > 0 ? Math.round((bill * tipPercent) / 100) : 0;
  const total = bill + (Number(receipt.tax_cents) || 0) + tip;
  const claimed = selections.reduce((sum, sel) => sum + (sel.total_cents || 0), 0);
  const paid = selections.filter((sel) => sel.paid);
  const paidCents = paid.reduce((sum, sel) => sum + (sel.total_cents || 0), 0);
  const unpaid = selections.filter((sel) => !sel.paid && sel.verifiable).length;
  const unknown = selections.filter((sel) => !sel.paid && !sel.verifiable).length;

  const head = document.createElement("div");
  head.className = "history-head";
  const titleBox = document.createElement("div");
  titleBox.className = "history-title";
  const when = document.createElement("strong");
  const created = new Date(receipt.created_at);
  when.textContent = isNaN(created)
    ? "Receipt"
    : created.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
  titleBox.appendChild(when);
  const meta = document.createElement("span");
  meta.className = "history-meta";
  meta.textContent = `${items.length} item${items.length === 1 ? "" : "s"} · ${currency}`;
  titleBox.appendChild(meta);
  head.appendChild(titleBox);
  const totalEl = document.createElement("span");
  totalEl.className = "history-total";
  totalEl.textContent = formatCents(total, currency);
  head.appendChild(totalEl);
  card.appendChild(head);

  const badges = document.createElement("div");
  badges.className = "history-badges";
  if (selections.length === 0) {
    badges.appendChild(historyBadge("No invoices yet"));
  } else {
    if (paid.length > 0) badges.appendChild(historyBadge(`${paid.length} paid · ${formatPlain(paidCents, currency)}`, "ok"));
    if (unpaid > 0) badges.appendChild(historyBadge(`${unpaid} not paid yet`, "wait"));
    if (unknown > 0) badges.appendChild(historyBadge(`${unknown} can’t confirm`));
  }
  const unclaimed = total - claimed;
  if (unclaimed > 0) badges.appendChild(historyBadge(`${formatPlain(unclaimed, currency)} unclaimed`, "wait"));
  if (selections.length > 0 && unclaimed <= 0) badges.appendChild(historyBadge("Fully claimed"));
  card.appendChild(badges);

  if (selections.length > 0) {
    const details = document.createElement("details");
    details.className = "history-details";
    const summary = document.createElement("summary");
    summary.textContent = `${selections.length} invoice${selections.length === 1 ? "" : "s"}`;
    details.appendChild(summary);
    const who = document.createElement("div");
    who.className = "who-list history-who";
    selections.forEach((sel) => who.appendChild(buildWhoCard(sel, currency)));
    details.appendChild(who);
    wireCopy(details);
    card.appendChild(details);
  }

  const actions = document.createElement("div");
  actions.className = "history-card-actions";
  const open = document.createElement("a");
  open.className = "btn btn-small";
  open.href = `/h/${entry.token}`;
  open.textContent = "Open receipt";
  actions.appendChild(open);
  actions.appendChild(removeButton);
  card.appendChild(actions);
  return card;
}

function showHistoryEmpty(list) {
  list.textContent = "";
  const empty = document.createElement("p");
  empty.className = "history-note";
  empty.textContent = "No receipts yet. Receipts you upload from this browser will appear here.";
  list.appendChild(empty);
}

async function loadHistory() {
  const list = document.getElementById("history-list");
  if (!list || historyLoading) return;
  const tokens = readHostHistory();
  if (tokens.length === 0) {
    showHistoryEmpty(list);
    return;
  }
  historyLoading = true;
  const refresh = document.getElementById("history-refresh");
  if (refresh) refresh.disabled = true;
  if (!list.querySelector(".history-card")) {
    list.textContent = "";
    const loading = document.createElement("p");
    loading.className = "history-note";
    loading.textContent = "Loading…";
    list.appendChild(loading);
  }
  try {
    const entries = await Promise.all(tokens.map(fetchHistoryEntry));
    // Newest receipt first, whatever order they were added in.
    entries.sort((a, b) => {
      const at = a.receipt ? Date.parse(a.receipt.created_at) || 0 : 0;
      const bt = b.receipt ? Date.parse(b.receipt.created_at) || 0 : 0;
      return bt - at;
    });
    const open = new Set(
      Array.from(list.querySelectorAll(".history-card[data-token]"))
        .filter((card) => card.querySelector("details[open]"))
        .map((card) => card.dataset.token)
    );
    list.textContent = "";
    entries.forEach((entry) => {
      const card = buildHistoryCard(entry);
      card.dataset.token = entry.token;
      if (open.has(entry.token)) {
        const details = card.querySelector("details");
        if (details) details.open = true;
      }
      list.appendChild(card);
    });
  } finally {
    historyLoading = false;
    if (refresh) refresh.disabled = false;
  }
}

function setupHistory() {
  syncHistoryLink();
  if (!isHistoryPage) return;

  const refresh = document.getElementById("history-refresh");
  if (refresh) refresh.addEventListener("click", loadHistory);

  // "/" reopens the last host receipt, so starting a new one forgets it first,
  // as "Start a new receipt" on the share view does.
  const startNew = document.getElementById("history-new");
  if (startNew) {
    startNew.addEventListener("click", () => {
      storage.remove("satsplit_host_token");
      window.location.href = "/";
    });
  }

  const addForm = document.getElementById("history-add");
  const addInput = document.getElementById("history-add-input");
  const addResult = document.getElementById("history-result");
  if (addForm && addInput) {
    addForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      clearResult(addResult);
      // Accept a host link, a share link, or the bare code.
      const match = addInput.value.trim().match(/([a-f0-9]{8,64})\/?$/i);
      if (!match) {
        showError(addResult, "That does not look like a receipt link or code.");
        return;
      }
      const entry = await fetchHistoryEntry(match[1].toLowerCase());
      if (!entry.receipt) {
        showError(addResult, "No receipt was found for that link.");
        return;
      }
      if (entry.receipt.is_host === false) {
        showError(addResult, "That is a guest link. Use the host link of a receipt you uploaded.");
        return;
      }
      rememberHostReceipt(entry.token);
      addInput.value = "";
      loadHistory();
    });
  }
}
