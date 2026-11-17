const viewUpload = document.getElementById("view-upload");
const viewCrop = document.getElementById("view-crop");
const viewReview = document.getElementById("view-review");
const viewShare = document.getElementById("view-share");
const viewJoin = document.getElementById("view-join");

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
const cropError = document.getElementById("crop-error");

const reviewImage = document.getElementById("review-image");
const reviewOCR = document.getElementById("review-ocr");
const editList = document.getElementById("edit-list");
const addItemButton = document.getElementById("add-item");
const saveItemsButton = document.getElementById("save-items");
const editResult = document.getElementById("edit-result");
const tipPercentInput = document.getElementById("tip-percent");
const taxAmountInput = document.getElementById("tax-amount");

const sharePanel = document.getElementById("share-panel");
const newUploadButton = document.getElementById("new-upload");

const selectionForm = document.getElementById("selection-form");
const selectionResult = document.getElementById("selection-result");
const itemsContainer = document.getElementById("items");
const subtotalEl = document.getElementById("subtotal");
const receiptSummary = document.getElementById("receipt-summary");

let currentToken = "";
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

const token = window.location.pathname.startsWith("/r/")
  ? window.location.pathname.split("/r/")[1].replace(/\/$/, "")
  : "";

const hostToken = window.location.pathname.startsWith("/h/")
  ? window.location.pathname.split("/h/")[1].replace(/\/$/, "")
  : "";

if (hostToken) {
  localStorage.setItem("satsplit_host_token", hostToken);
  restoreHostSession(hostToken);
} else if (token) {
  setView(viewJoin);
  loadReceipt(token);
} else {
  const savedToken = localStorage.getItem("satsplit_host_token");
  if (savedToken) {
    restoreHostSession(savedToken);
  } else {
    setView(viewUpload);
    if (lnInput) {
      const cached = localStorage.getItem("satsplit_ln_address");
      if (cached) {
        lnInput.value = cached;
      }
    }
  }
}

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
      localStorage.setItem("satsplit_ln_address", lnInput.value.trim());
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
        cropImage.src = reader.result;
        cropRect = null;
        if (cropOverlay) {
          cropOverlay.classList.add("hidden");
          cropOverlay.style.width = "0px";
          cropOverlay.style.height = "0px";
        }
      }
      setView(viewCrop);
    };
    reader.readAsDataURL(pendingUpload.file);
  });
}

if (cropper) {
  let dragging = false;
  let startX = 0;
  let startY = 0;

  const updateOverlay = (x, y, w, h, offsetX, offsetY) => {
    if (!cropOverlay) return;
    cropOverlay.classList.remove("hidden");
    cropOverlay.style.left = `${x + offsetX}px`;
    cropOverlay.style.top = `${y + offsetY}px`;
    cropOverlay.style.width = `${w}px`;
    cropOverlay.style.height = `${h}px`;
  };

  const imageRect = () => {
    if (!cropImage || !cropImage.src) return null;
    const rect = cropImage.getBoundingClientRect();
    if (rect.width === 0 || rect.height === 0) return null;
    return rect;
  };

  cropper.addEventListener("pointerdown", (event) => {
    const rect = imageRect();
    if (!rect) return;
    dragging = true;
    cropper.setPointerCapture(event.pointerId);
    startX = Math.max(0, Math.min(event.clientX - rect.left, rect.width));
    startY = Math.max(0, Math.min(event.clientY - rect.top, rect.height));
    const parentRect = cropper.getBoundingClientRect();
    const offsetX = rect.left - parentRect.left;
    const offsetY = rect.top - parentRect.top;
    updateOverlay(startX, startY, 1, 1, offsetX, offsetY);
  });

  cropper.addEventListener("pointermove", (event) => {
    if (!dragging) return;
    const rect = imageRect();
    if (!rect) return;
    const currentX = Math.max(0, Math.min(event.clientX - rect.left, rect.width));
    const currentY = Math.max(0, Math.min(event.clientY - rect.top, rect.height));
    const x = Math.min(startX, currentX);
    const y = Math.min(startY, currentY);
    const w = Math.abs(currentX - startX);
    const h = Math.abs(currentY - startY);
    const parentRect = cropper.getBoundingClientRect();
    const offsetX = rect.left - parentRect.left;
    const offsetY = rect.top - parentRect.top;
    updateOverlay(x, y, w, h, offsetX, offsetY);
    cropRect = { x, y, w, h };
  });

  cropper.addEventListener("pointerup", (event) => {
    dragging = false;
    cropper.releasePointerCapture(event.pointerId);
  });

  cropper.addEventListener("pointerleave", () => {
    dragging = false;
  });
}

if (cropImage) {
  cropImage.setAttribute("draggable", "false");
  cropImage.addEventListener("dragstart", (event) => {
    event.preventDefault();
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
      showError(cropError, "Tap and drag to select a crop area, or skip.");
      return;
    }
    submitUpload(cropped, pendingUpload.file.name);
  });
}

if (selectionForm) {
  const participantInput = selectionForm.querySelector("input[name=participant]");
  const cachedName = localStorage.getItem("satsplit_participant_name");
  if (cachedName && participantInput) {
    participantInput.value = cachedName;
  }

  selectionForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    clearResult(selectionResult);
    const participant = participantInput ? participantInput.value.trim() : "";
    if (!participant) {
      showError(selectionResult, "Enter your name first.");
      if (participantInput) participantInput.focus();
      return;
    }
    localStorage.setItem("satsplit_participant_name", participant);
    const selected = Array.from(itemsContainer.querySelectorAll("input[type=checkbox]:checked"));
    if (selected.length === 0) {
      showError(selectionResult, "Select at least one item.");
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
    const prev = JSON.parse(localStorage.getItem(storageKey) || "null");
    const body = { participant, item_ids: itemIds, quantities };
    if (prev && prev.selection_id) {
      body.replace_selection_id = prev.selection_id;
      if (prev.edit_secret) body.edit_secret = prev.edit_secret;
    }
    try {
      const response = await fetch(`/api/receipt/${token}/selection`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const payload = await response.json();
      if (!response.ok) {
        showError(selectionResult, payload.error || "Invoice failed");
        return;
      }
      localStorage.setItem(storageKey, JSON.stringify({
        selection_id: payload.selection_id,
        edit_secret: payload.edit_secret || "",
        item_ids: itemIds,
        quantities,
      }));
      renderSelectionResult(payload);
    } catch (err) {
      showError(selectionResult, "Invoice failed");
    }
  });
}

if (editList) {
  editList.addEventListener("input", () => {
    editDirty = true;
  });
}

if (taxAmountInput) {
  taxAmountInput.addEventListener("input", () => {
    taxDirty = true;
    currentTaxCents = readTaxCents();
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
      showSuccess(editResult, "Items updated.");
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
    localStorage.removeItem("satsplit_host_token");
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
  ["crop-skip", "crop-apply"].forEach((id) => {
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
      text.textContent = "Uploading\u2026";
    } else if (seconds < 40) {
      text.textContent = `Reading the receipt\u2026 ${seconds}s (usually under 30 seconds)`;
    } else {
      text.textContent = `Still reading\u2026 ${seconds}s. Long receipts take a little longer.`;
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

async function restoreHostSession(savedToken) {
  try {
    const response = await fetch(`/api/receipt/${savedToken}`);
    if (!response.ok) {
      localStorage.removeItem("satsplit_host_token");
      setView(viewUpload);
      return;
    }
    const payload = await response.json();
    currentToken = savedToken;
    currentCurrency = payload.currency_code || "USD";
    const items = payload.items || [];

    if (reviewImage && payload.image_url) {
      reviewImage.src = payload.image_url;
    }
    if (reviewOCR) {
      reviewOCR.textContent = payload.ocr_text || "";
    }
    renderEditList(items);
    currentItemIdentity = itemIdentity(items);
    setReviewStatus(payload);
    if (tipPercentInput) {
      tipPercentInput.value = payload.tip_percent || 0;
    }
    setTaxAmount(payload.tax_cents);
    currentBillCents = billCentsOf(items);

    if (items.length > 0) {
      updateSharePanel(items);
      setView(viewShare);
      startUpdatePolling();
    } else {
      setView(viewReview);
    }
  } catch (err) {
    localStorage.removeItem("satsplit_host_token");
    setView(viewUpload);
  }
}

async function loadReceipt(receiptToken) {
  try {
    const response = await fetch(`/api/receipt/${receiptToken}`);
    const payload = await response.json();
    if (!response.ok) {
      showError(selectionResult, payload.error || "Receipt not found");
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
    showError(selectionResult, "Receipt not found");
  }
}

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
    applyPaidStatus(payload.selections || []);
  } catch (err) {
    // silently ignore polling errors
  }
}

function applyPaidStatus(selections) {
  if (!itemsContainer) return;

  // Determine which selection belongs to the current user
  const storageKey = `satsplit_selection_${token}`;
  const myData = JSON.parse(localStorage.getItem(storageKey) || "null");
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
      let filled = 0;
      let filledMine = 0;
      for (let s = 0; s < splitCount && idx < pool.length; s++) {
        if (pool[idx].mine) filledMine++;
        idx++;
        filled++;
      }

      let badge = row.querySelector(".item-paid-badge");

      if (filled >= splitCount && filledMine >= splitCount) {
        // fully claimed by me — keep enabled + checked, show "yours"
        checkbox.disabled = false;
        checkbox.checked = true;
        row.classList.remove("paid");
        row.classList.add("mine");
        if (!badge) {
          badge = document.createElement("span");
          badge.className = "item-paid-badge";
          row.appendChild(badge);
        }
        badge.textContent = "yours";
      } else if (filled >= splitCount) {
        // fully claimed by others — disable
        if (!checkbox.disabled) {
          checkbox.disabled = true;
          if (checkbox.checked) {
            checkbox.checked = false;
            changed = true;
          }
        }
        row.classList.add("paid");
        row.classList.remove("mine");
        if (!badge) {
          badge = document.createElement("span");
          badge.className = "item-paid-badge";
          row.appendChild(badge);
        }
        badge.textContent = "paid";
      } else if (filled > 0 && splitCount > 1) {
        // partially claimed split item — keep enabled
        checkbox.disabled = false;
        row.classList.remove("paid");
        row.classList.remove("mine");
        if (filledMine > 0) {
          checkbox.checked = true;
        }
        if (!badge) {
          badge = document.createElement("span");
          badge.className = "item-paid-badge";
          row.appendChild(badge);
        }
        badge.textContent = `${filled}/${splitCount} paid`;
      } else {
        // unclaimed
        checkbox.disabled = false;
        row.classList.remove("paid");
        row.classList.remove("mine");
        if (badge) badge.remove();
      }
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
    row.className = "item-row";
    row.dataset.label = item.label || "Item";

    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
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
      syncQuantityRow(row);
      updateSubtotal();
    });

    const label = document.createElement("label");
    label.textContent = item.label || "Item";
    if (quantity > 1) {
      const qtyTag = document.createElement("span");
      qtyTag.className = "item-qty-tag";
      qtyTag.textContent = ` \u00d7${quantity}`;
      label.appendChild(qtyTag);
    }
    if (item.translated_label && item.translated_label !== item.label) {
      const tl = document.createElement("span");
      tl.className = "translated-label";
      tl.textContent = ` \u2014 ${item.translated_label}`;
      label.appendChild(tl);
    }

    const amountWrap = document.createElement("div");
    amountWrap.className = "item-amount-wrap";

    const amount = document.createElement("span");
    amount.textContent = formatCents(item.amount_cents);
    amountWrap.appendChild(amount);

    const splitCount = item.split_count || 1;
    const eachSpan = document.createElement("span");
    eachSpan.className = "item-each";
    if (quantity > 1 && splitCount > 1) {
      eachSpan.textContent = `${formatCents(splitShare(unitsCents(item.amount_cents, 1, quantity), splitCount))} per share of one`;
    } else if (quantity > 1) {
      eachSpan.textContent = `${formatCents(unitsCents(item.amount_cents, 1, quantity))} each`;
    } else if (splitCount > 1) {
      eachSpan.textContent = `${formatCents(splitShare(item.amount_cents, splitCount))} each`;
    }
    amountWrap.appendChild(eachSpan);

    // Quantity picker: how many of this item the person ordered.
    let qtyWrap = null;
    if (quantity > 1) {
      qtyWrap = document.createElement("div");
      qtyWrap.className = "guest-qty-wrap guest-split-stepper";

      const qtyMinus = document.createElement("button");
      qtyMinus.type = "button";
      qtyMinus.className = "guest-split-btn guest-qty-minus";
      qtyMinus.textContent = "\u2212";
      qtyMinus.setAttribute("aria-label", "Fewer");

      const qtyVal = document.createElement("span");
      qtyVal.className = "guest-split-value guest-qty-value";

      const qtyPlus = document.createElement("button");
      qtyPlus.type = "button";
      qtyPlus.className = "guest-split-btn guest-qty-plus";
      qtyPlus.textContent = "+";
      qtyPlus.setAttribute("aria-label", "More");

      qtyMinus.addEventListener("click", (e) => {
        e.stopPropagation();
        if (checkbox.disabled || !checkbox.checked) return;
        const units = selectedUnits(checkbox);
        if (units > 1) {
          checkbox.dataset.units = units - 1;
        } else {
          checkbox.checked = false;
        }
        row.dataset.touched = "1";
        syncQuantityRow(row);
        updateSubtotal();
      });
      qtyPlus.addEventListener("click", (e) => {
        e.stopPropagation();
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
        updateSubtotal();
      });

      qtyWrap.appendChild(qtyMinus);
      qtyWrap.appendChild(qtyVal);
      qtyWrap.appendChild(qtyPlus);
    }

    // Split controls: toggle button reveals stepper
    const splitWrap = document.createElement("div");
    splitWrap.className = "guest-split-wrap";

    const splitToggle = document.createElement("button");
    splitToggle.type = "button";
    splitToggle.className = "guest-split-toggle";
    splitToggle.textContent = "Split";

    const splitStepper = document.createElement("div");
    splitStepper.className = "guest-split-stepper";

    const splitMinus = document.createElement("button");
    splitMinus.type = "button";
    splitMinus.className = "guest-split-btn";
    splitMinus.textContent = "\u2212";
    splitMinus.setAttribute("aria-label", "Decrease split");

    const splitVal = document.createElement("span");
    splitVal.className = "guest-split-value";
    splitVal.textContent = splitCount;

    const splitWay = document.createElement("span");
    splitWay.className = "stepper-way";
    splitWay.textContent = "way";

    const splitPlus = document.createElement("button");
    splitPlus.type = "button";
    splitPlus.className = "guest-split-btn";
    splitPlus.textContent = "+";
    splitPlus.setAttribute("aria-label", "Increase split");

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
        }
      } catch (err) {
        // silently ignore
      }
      btn.disabled = false;
    };

    splitMinus.addEventListener("click", (e) => {
      e.stopPropagation();
      sendSplitDelta(-1, splitMinus);
    });
    splitPlus.addEventListener("click", (e) => {
      e.stopPropagation();
      sendSplitDelta(1, splitPlus);
    });

    splitStepper.appendChild(splitMinus);
    splitStepper.appendChild(splitVal);
    splitStepper.appendChild(splitWay);
    splitStepper.appendChild(splitPlus);

    splitToggle.addEventListener("click", (e) => {
      e.stopPropagation();
      splitWrap.classList.add("open");
    });

    splitWrap.appendChild(splitToggle);
    splitWrap.appendChild(splitStepper);

    // If already split > 1, show the stepper directly
    if (splitCount > 1) {
      splitWrap.classList.add("open");
    }

    row.appendChild(checkbox);
    row.appendChild(label);
    row.appendChild(amountWrap);
    // Quantity and split are different things and can combine: the unit
    // picker says how many this person had, the split says how many people
    // share each one (two shared appetizers between four people).
    if (qtyWrap) row.appendChild(qtyWrap);
    row.appendChild(splitWrap);

    row.addEventListener("click", (e) => {
      if (e.target === checkbox || e.target.closest(".guest-split-wrap") || e.target.closest(".guest-qty-wrap")) return;
      if (checkbox.disabled) return;
      checkbox.checked = !checkbox.checked;
      row.dataset.touched = "1";
      syncQuantityRow(row);
      updateSubtotal();
    });

    syncQuantityRow(row);
    itemsContainer.appendChild(row);
  });
  updateSubtotal();
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
  valueEl.textContent = `${shown} of ${itemQuantity(checkbox)}`;
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
  let badge = row.querySelector(".item-paid-badge");
  const ensureBadge = () => {
    if (!badge) {
      badge = document.createElement("span");
      badge.className = "item-paid-badge";
      row.appendChild(badge);
    }
    return badge;
  };

  if (available <= 0) {
    if (checkbox.checked) {
      checkbox.checked = false;
      changed = true;
    }
    checkbox.disabled = true;
    row.classList.add("paid");
    row.classList.remove("mine");
    ensureBadge().textContent = "paid";
  } else {
    checkbox.disabled = false;
    row.classList.remove("paid");
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
    row.classList.toggle("mine", mine > 0 && others + mine >= capacity);
    if (others + mine > 0) {
      ensureBadge().textContent = mine > 0 && others === 0 && split === 1
        ? `yours: ${mine} of ${quantity}`
        : `${others + mine}/${capacity} claimed`;
    } else if (badge) {
      badge.remove();
    }
  }
  syncQuantityRow(row);
  return changed;
}

function updateSubtotal() {
  const selected = Array.from(itemsContainer.querySelectorAll("input[type=checkbox]:checked"));
  let food = 0;
  selected.forEach((input) => {
    const amount = Number(input.dataset.amount || 0);
    const split = Number(input.dataset.split || 1);
    food += splitShare(unitsCents(amount, selectedUnits(input), itemQuantity(input)), split);
  });
  const tax = taxShareCents(currentTaxCents, food, currentBillCents);
  const tipPct = readTipPercent();
  const tip = tipPct > 0 ? Math.round(food * tipPct / 100) : 0;
  if (subtotalEl) subtotalEl.textContent = formatCents(food);
  const taxRow = document.getElementById("guest-tax-row");
  const taxEl = document.getElementById("guest-tax");
  const totalEl = document.getElementById("guest-total");
  if (taxRow && taxEl) {
    taxEl.textContent = formatCents(tax);
    taxRow.classList.toggle("hidden", currentTaxCents <= 0);
  }
  if (totalEl) totalEl.textContent = formatCents(food + tax + tip);
}

function renderUploadResult(payload) {
  currentToken = payload.token;
  currentCurrency = payload.currency_code || "USD";
  localStorage.setItem("satsplit_host_token", currentToken);
  uploadResult.classList.remove("hidden");
  uploadResult.classList.remove("error");

  uploadResult.innerHTML = `Receipt uploaded. Review items below.`;

  if (reviewImage && payload.image_url) {
    reviewImage.src = payload.image_url;
  }
  if (reviewOCR) {
    reviewOCR.textContent = payload.ocr_text || "";
  }
  renderEditList(payload.items || []);
  currentItemIdentity = itemIdentity(payload.items || []);
  setReviewStatus(payload);
  if (tipPercentInput) {
    tipPercentInput.value = payload.tip_percent || 0;
  }
  setTaxAmount(payload.tax_cents);
  currentBillCents = billCentsOf(payload.items || []);
  startUpdatePolling();
}

function renderSelectionResult(payload) {
  selectionResult.classList.remove("hidden");
  selectionResult.classList.remove("error");
  currentCurrency = payload.currency_code || currentCurrency;
  selectionResult.innerHTML = `
    <strong>${payload.already_paid_cents > 0 ? "Second invoice ready" : "Invoice ready"}</strong>
    <div class="copy-row">
      <input type="text" value="${payload.invoice}" readonly />
      <button type="button" data-copy="${payload.invoice}">Copy</button>
    </div>
    <p>Subtotal: ${formatCents(payload.subtotal_cents)} · Tax and service ${formatCents(payload.tax_cents || 0)} · Tip ${payload.tip_percent || 0}% (${formatCents(payload.tip_cents || 0)})</p>
    <p>Total: ${formatCents(payload.total_cents)}${
      payload.already_paid_cents > 0
        ? ` · already paid ${formatCents(payload.already_paid_cents)} · <strong>this second invoice: ${formatCents(payload.invoice_cents)}</strong>`
        : ""
    } · ${payload.amount_msat} msat</p>
  `;
  wireCopy(selectionResult);
}

function updateSharePanel(items) {
  if (!sharePanel) return;
  shareItems = items;
  const shareURL = `${window.location.origin}/r/${currentToken}`;

  sharePanel.textContent = "";

  const heading = document.createElement("strong");
  heading.textContent = "Share this with your group";
  sharePanel.appendChild(heading);

  const qrDiv = document.createElement("div");
  qrDiv.className = "qr";

  const qrImg = document.createElement("img");
  qrImg.src = `/api/receipt/${currentToken}/qr`;
  qrImg.alt = "Share QR";
  qrDiv.appendChild(qrImg);

  const infoDiv = document.createElement("div");

  const copyRow = document.createElement("div");
  copyRow.className = "copy-row";
  const urlInput = document.createElement("input");
  urlInput.type = "text";
  urlInput.value = shareURL;
  urlInput.readOnly = true;
  copyRow.appendChild(urlInput);
  const copyBtn = document.createElement("button");
  copyBtn.type = "button";
  copyBtn.setAttribute("data-copy", shareURL);
  copyBtn.textContent = "Copy";
  copyRow.appendChild(copyBtn);
  infoDiv.appendChild(copyRow);

  qrDiv.appendChild(infoDiv);
  sharePanel.appendChild(qrDiv);

  const hostHint = document.createElement("p");
  hostHint.className = "host-url-hint";
  hostHint.textContent = "Bookmark this page to return as host.";
  sharePanel.appendChild(hostHint);

  if (window.history && window.history.replaceState) {
    window.history.replaceState(null, "", `/h/${currentToken}`);
  }

  const itemsList = document.createElement("div");
  itemsList.id = "share-items-list";
  items.forEach((item) => {
    const row = document.createElement("div");
    row.className = "share-item-row";
    row.dataset.itemId = item.id;
    row.dataset.splitCount = item.split_count || 1;
    row.dataset.quantity = item.quantity || 1;

    const label = document.createElement("span");
    label.className = "share-item-label";
    label.textContent = (item.quantity || 1) > 1 ? `${item.label || "Item"} \u00d7${item.quantity}` : item.label || "Item";
    if (item.translated_label && item.translated_label !== item.label) {
      const tl = document.createElement("span");
      tl.className = "translated-label";
      tl.textContent = ` \u2014 ${item.translated_label}`;
      label.appendChild(tl);
    }
    row.appendChild(label);

    const amount = document.createElement("span");
    amount.className = "share-item-amount";
    amount.textContent = formatCents(item.amount_cents);
    row.appendChild(amount);

    const status = document.createElement("span");
    status.className = "share-item-status";
    status.textContent = "waiting";
    row.appendChild(status);

    itemsList.appendChild(row);
  });
  sharePanel.appendChild(itemsList);

  const receiptTotal = billCentsOf(items);
  currentBillCents = receiptTotal;
  const taxCents = currentTaxCents;
  const tipPct = readTipPercent();
  const tipCents = tipPct > 0 ? Math.round(receiptTotal * tipPct / 100) : 0;

  const totalsDiv = document.createElement("div");
  totalsDiv.id = "share-totals";
  totalsDiv.className = "share-totals";

  const addTotalRow = (label, value, className) => {
    const row = document.createElement("div");
    row.className = "total-row" + (className ? " " + className : "");
    const labelSpan = document.createElement("span");
    labelSpan.textContent = label;
    const valueSpan = document.createElement("span");
    valueSpan.textContent = value;
    if (className === "total-row-claimed") valueSpan.id = "share-claimed";
    if (className === "total-row-remaining") valueSpan.id = "share-remaining";
    row.appendChild(labelSpan);
    row.appendChild(valueSpan);
    totalsDiv.appendChild(row);
  };

  addTotalRow("Subtotal", formatCents(receiptTotal));
  if (taxCents > 0) addTotalRow("Tax and service", formatCents(taxCents));
  if (tipPct > 0) addTotalRow("Tip (" + tipPct + "%)", formatCents(tipCents));
  addTotalRow("Total", formatCents(receiptTotal + taxCents + tipCents), "total-row-grand");
  addTotalRow("Claimed", formatCents(0), "total-row-claimed");
  addTotalRow("Remaining", formatCents(receiptTotal + taxCents + tipCents), "total-row-remaining");

  const disclaimer = document.createElement("p");
  disclaimer.className = "share-totals-disclaimer";
  disclaimer.textContent = "SatSplit can only track invoice creation, not payment.";
  totalsDiv.appendChild(disclaimer);

  sharePanel.appendChild(totalsDiv);

  const selectionsDetail = document.createElement("div");
  selectionsDetail.id = "share-selections-detail";
  selectionsDetail.className = "share-selections-detail";
  sharePanel.appendChild(selectionsDetail);

  wireCopy(sharePanel);
  startSelectionsPolling();
}

function startSelectionsPolling() {
  if (selectionsInterval) clearInterval(selectionsInterval);
  fetchSelections();
  selectionsInterval = setInterval(fetchSelections, 5000);
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
    const label = row.querySelector(".share-item-label").textContent;
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
      const statusEl = row.querySelector(".share-item-status");
      if (!statusEl) return;
      const rowQuantity = Number(row.dataset.quantity) || 1;
      const slots = rowQuantity * (Number(row.dataset.splitCount) || 1);
      const assigned = [];
      for (let s = 0; s < slots && idx < pool.length; s++) {
        assigned.push(pool[idx++]);
      }
      const full = assigned.length >= slots;
      row.classList.toggle("claimed", assigned.length > 0 && full);
      row.classList.toggle("partly-claimed", assigned.length > 0 && !full);
      if (assigned.length > 0) {
        // One entry per person, in the order they first claimed: "Ana ×2, Ben".
        const counts = new Map();
        assigned.forEach((n) => counts.set(n, (counts.get(n) || 0) + 1));
        const names = Array.from(counts, ([n, c]) => (c > 1 ? `${n} \u00d7${c}` : n)).join(", ");
        const prefix = slots > 1 ? `${assigned.length}/${slots} ` : "";
        statusEl.textContent = (full ? "\u2713 " : "") + prefix + names;
      } else {
        statusEl.textContent = slots > 1 ? `0/${slots} waiting` : "waiting";
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
    if (remainingEl) remainingEl.textContent = formatCents(grandTotal - claimedCents);
  }

  // Render per-selection detail
  const detailContainer = document.getElementById("share-selections-detail");
  const detailKey = JSON.stringify(selections.map((sel) => [sel.id, sel.total_cents, sel.paid, sel.verifiable, sel.invoice]));
  if (detailContainer && detailContainer.dataset.key !== detailKey) {
    detailContainer.dataset.key = detailKey;
    detailContainer.textContent = "";
    if (selections.length > 0) {
      const heading = document.createElement("strong");
      heading.textContent = "Invoices";
      detailContainer.appendChild(heading);
    }
    selections.forEach((sel) => {
      const entry = document.createElement("div");
      entry.className = "selection-entry";

      const header = document.createElement("div");
      header.className = "selection-entry-header";
      const name = document.createElement("span");
      name.textContent = sel.parent_id ? `${sel.participant} (added items)` : sel.participant;
      const amount = document.createElement("span");
      amount.textContent = formatCents(sel.total_cents || 0);
      header.appendChild(name);
      header.appendChild(amount);
      entry.appendChild(header);

      // Paid status comes from the provider (LUD-21). Without it the host
      // matches the payment hash against their wallet's payment list.
      const badge = document.createElement("span");
      badge.className = "selection-status";
      if (sel.paid) {
        badge.classList.add("selection-status-paid");
        badge.textContent = "\u2713 paid";
      } else if (sel.verifiable) {
        badge.classList.add("selection-status-unpaid");
        badge.textContent = "not paid yet";
      } else {
        badge.classList.add("selection-status-unknown");
        badge.textContent = "can\u2019t confirm";
        badge.title = "This Lightning address provider does not report payments. Match the payment hash in your wallet.";
      }
      header.appendChild(badge);

      const meta = document.createElement("div");
      meta.className = "selection-entry-meta";
      const addCopy = (text, value) => {
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "invoice-copy-btn";
        btn.setAttribute("data-copy", value);
        btn.textContent = text;
        meta.appendChild(btn);
      };
      if (sel.amount_sats > 0) {
        const sats = document.createElement("span");
        sats.textContent = `${Number(sel.amount_sats).toLocaleString()} sats`;
        meta.appendChild(sats);
      }
      if (sel.payment_hash) {
        const hash = document.createElement("span");
        hash.className = "selection-hash";
        hash.title = sel.payment_hash;
        hash.textContent = `hash ${sel.payment_hash.slice(0, 8)}\u2026${sel.payment_hash.slice(-8)}`;
        meta.appendChild(hash);
        addCopy("copy hash", sel.payment_hash);
      }
      if (sel.preimage) addCopy("copy preimage", sel.preimage);
      if (sel.invoice) addCopy("copy invoice", sel.invoice);
      if (meta.childNodes.length > 0) entry.appendChild(meta);

      detailContainer.appendChild(entry);
    });
    wireCopy(detailContainer);
  }
}

function setView(activeView) {
  if (activeView !== viewShare && selectionsInterval) {
    clearInterval(selectionsInterval);
    selectionsInterval = null;
  }
  if (activeView === viewUpload && updatePollInterval) {
    clearInterval(updatePollInterval);
    updatePollInterval = null;
  }
  [viewUpload, viewCrop, viewReview, viewShare, viewJoin].forEach((view) => {
    if (!view) return;
    if (view === activeView) {
      view.classList.remove("hidden");
    } else {
      view.classList.add("hidden");
    }
  });
}

function wireCopy(scope) {
  scope.querySelectorAll("button[data-copy]").forEach((button) => {
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

function showSuccess(el, message) {
  if (!el) return;
  el.classList.remove("hidden");
  el.classList.remove("error");
  el.textContent = message;
}

function clearResult(el) {
  if (!el) return;
  el.classList.add("hidden");
  el.classList.remove("error");
  el.textContent = "";
}

function formatCents(cents) {
  const value = (cents / 100);
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: currentCurrency || "USD",
    }).format(value);
  } catch (err) {
    return `$${value.toFixed(2)}`;
  }
}

function renderEditList(items) {
  if (!editList) return;
  editDirty = false;
  editList.innerHTML = "";
  if (!items || items.length === 0) {
    addEditRow({ label: "", amount_cents: 0, split_count: 1, quantity: 1 });
    return;
  }
  items.forEach((item) => addEditRow(item));
}

function addEditRow(item) {
  if (!editList) return;
  const row = document.createElement("div");
  row.className = "item-row edit-row";

  // --- Display spans (collapsed state) ---
  const displayLabel = document.createElement("span");
  displayLabel.className = "edit-display edit-display-label";
  displayLabel.textContent = item.label || "Item";

  const displayAmount = document.createElement("span");
  displayAmount.className = "edit-display edit-display-amount";
  displayAmount.textContent = formatCents(item.amount_cents || 0);

  // --- Input fields (expanded state) ---
  const labelInput = document.createElement("input");
  labelInput.type = "text";
  labelInput.className = "edit-field";
  labelInput.placeholder = "Item name";
  labelInput.value = item.label || "";

  const amountInput = document.createElement("input");
  amountInput.type = "text";
  amountInput.className = "edit-field";
  amountInput.inputMode = "decimal";
  amountInput.pattern = "[0-9]*\\.?[0-9]*";
  amountInput.placeholder = "0.00";
  amountInput.value = (item.amount_cents || 0) / 100;

  // --- Quantity (units on this line; amount is the total for all of them) ---
  const qtyLabel = document.createElement("label");
  qtyLabel.className = "edit-qty-label";
  qtyLabel.textContent = "Qty ";
  const qtyInput = document.createElement("input");
  qtyInput.type = "number";
  qtyInput.className = "edit-qty";
  qtyInput.min = "1";
  qtyInput.max = "999";
  qtyInput.step = "1";
  qtyInput.inputMode = "numeric";
  qtyInput.value = item.quantity || 1;
  qtyLabel.appendChild(qtyInput);

  const displayText = () => {
    const q = Math.round(Number(qtyInput.value)) || 1;
    const name = labelInput.value || "Item";
    return q > 1 ? `${name} \u00d7${q}` : name;
  };
  displayLabel.textContent = displayText();

  // --- Stepper widget for split count ---
  const stepperWrap = document.createElement("div");
  stepperWrap.className = "stepper-wrap";
  const splitValue = item.split_count || 1;

  const splitToggle = document.createElement("button");
  splitToggle.type = "button";
  splitToggle.className = "edit-split-toggle";
  splitToggle.textContent = "Split";
  splitToggle.addEventListener("click", () => {
    stepperWrap.classList.add("open");
  });

  const stepper = document.createElement("div");
  stepper.className = "stepper";

  const minusBtn = document.createElement("button");
  minusBtn.type = "button";
  minusBtn.className = "stepper-btn";
  minusBtn.textContent = "\u2212";
  minusBtn.setAttribute("aria-label", "Decrease split");

  const valueSpan = document.createElement("span");
  valueSpan.className = "stepper-value";
  valueSpan.textContent = splitValue;

  const waySuffix = document.createElement("span");
  waySuffix.className = "stepper-way";
  waySuffix.textContent = "way";

  const plusBtn = document.createElement("button");
  plusBtn.type = "button";
  plusBtn.className = "stepper-btn";
  plusBtn.textContent = "+";
  plusBtn.setAttribute("aria-label", "Increase split");

  minusBtn.addEventListener("click", () => {
    editDirty = true;
    const cur = Number(valueSpan.textContent) || 1;
    if (cur > 1) valueSpan.textContent = cur - 1;
  });
  plusBtn.addEventListener("click", () => {
    editDirty = true;
    const cur = Number(valueSpan.textContent) || 1;
    valueSpan.textContent = cur + 1;
  });

  stepper.appendChild(minusBtn);
  stepper.appendChild(valueSpan);
  stepper.appendChild(waySuffix);
  stepper.appendChild(plusBtn);

  stepperWrap.appendChild(splitToggle);
  stepperWrap.appendChild(stepper);

  if (splitValue > 1) {
    stepperWrap.classList.add("open");
  }

  // --- Remove button (expanded only) ---
  const removeButton = document.createElement("button");
  removeButton.type = "button";
  removeButton.className = "edit-remove-btn edit-field";
  removeButton.textContent = "Remove";
  removeButton.addEventListener("click", () => {
    editDirty = true;
    row.remove();
  });

  // --- Pencil button (collapsed only) ---
  const pencilBtn = document.createElement("button");
  pencilBtn.type = "button";
  pencilBtn.className = "edit-pencil-btn edit-display";
  pencilBtn.textContent = "\u270F";
  pencilBtn.setAttribute("aria-label", "Edit item");
  pencilBtn.addEventListener("click", () => {
    row.classList.add("editing");
    labelInput.focus();
  });

  // --- Done button (expanded only) ---
  const doneBtn = document.createElement("button");
  doneBtn.type = "button";
  doneBtn.className = "edit-done-btn edit-field";
  doneBtn.textContent = "\u2713 Done";
  doneBtn.addEventListener("click", () => {
    row.classList.remove("editing");
    displayLabel.textContent = displayText();
    const cents = Math.round(Number(amountInput.value) * 100);
    displayAmount.textContent = formatCents(cents);
  });

  // --- Label cell (holds input + translated label) ---
  const labelCell = document.createElement("div");
  labelCell.className = "edit-label-cell edit-field";
  labelCell.appendChild(labelInput);
  labelCell.appendChild(qtyLabel);
  if (item.translated_label && item.translated_label !== item.label) {
    const tl = document.createElement("span");
    tl.className = "translated-label";
    tl.textContent = item.translated_label;
    labelCell.appendChild(tl);
  }

  // --- Assemble row ---
  row.appendChild(displayLabel);
  row.appendChild(displayAmount);
  row.appendChild(labelCell);
  row.appendChild(amountInput);
  row.appendChild(stepperWrap);
  row.appendChild(removeButton);
  row.appendChild(pencilBtn);
  row.appendChild(doneBtn);

  // New rows with empty label start expanded
  if (!item.label) {
    row.classList.add("editing");
  }

  editList.appendChild(row);
}

function itemIdentity(items) {
  return (items || [])
    .map((item) => `${item.id || ""}|${item.label}|${item.amount_cents}|${item.split_count || 1}|${item.quantity || 1}`)
    .join("\n");
}

function setReviewStatus(payload) {
  const el = document.getElementById("review-status");
  if (!el || !payload) return;
  if (payload.refining) {
    el.textContent = "Checking the receipt again for a more accurate item list. You can edit now; saving keeps your version.";
    return;
  }
  if ((payload.items || []).length === 0) {
    el.textContent = "No items detected. Add them below.";
    return;
  }
  el.textContent = "";
}

function readEditItems() {
  if (!editList) return [];
  const rows = Array.from(editList.querySelectorAll(".edit-row"));
  const items = [];
  for (const row of rows) {
    const labelInput = row.querySelector(".edit-label-cell input");
    const amountInput = row.querySelector("input[inputmode='decimal']");
    if (!labelInput || !amountInput) continue;
    const label = labelInput.value.trim();
    const amountValue = Number(amountInput.value);
    const stepperValue = row.querySelector(".stepper-value");
    const splitValue = stepperValue ? Number(stepperValue.textContent) : 1;
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
    const qtyInput = row.querySelector(".edit-qty");
    let quantity = qtyInput ? Math.round(Number(qtyInput.value)) : 1;
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

function applyTranslations(items) {
  // Update edit list translated labels
  if (editList) {
    const rows = Array.from(editList.querySelectorAll(".edit-row"));
    items.forEach((item, i) => {
      if (i >= rows.length) return;
      if (!item.translated_label || item.translated_label === item.label) return;
      const cell = rows[i].querySelector(".edit-label-cell");
      if (!cell) return;
      if (cell.querySelector(".translated-label")) return;
      const tl = document.createElement("span");
      tl.className = "translated-label";
      tl.textContent = item.translated_label;
      cell.appendChild(tl);
    });
  }

  // Update share panel translated labels
  const shareList = document.getElementById("share-items-list");
  if (shareList) {
    const rows = Array.from(shareList.querySelectorAll(".share-item-row"));
    items.forEach((item, i) => {
      if (i >= rows.length) return;
      if (!item.translated_label || item.translated_label === item.label) return;
      const label = rows[i].querySelector(".share-item-label");
      if (!label || label.querySelector(".translated-label")) return;
      const tl = document.createElement("span");
      tl.className = "translated-label";
      tl.textContent = ` \u2014 ${item.translated_label}`;
      label.appendChild(tl);
    });
  }
}
