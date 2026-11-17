import { useEffect, useId, useRef, type MouseEvent } from 'react';

interface ConfirmDialogProps {
  open: boolean;
  title: string;
  message: string;
  confirmLabel?: string;
  busy?: boolean;
  onConfirm: () => void | Promise<void>;
  onCancel: () => void;
}

// In-app replacement for window.confirm(). Clicking the backdrop or
// pressing Escape is the same as Cancel; nothing destructive happens
// until the confirm button is clicked.
export default function ConfirmDialog({
  open,
  title,
  message,
  confirmLabel = 'Delete',
  busy = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  const titleId = useId();
  const messageId = useId();
  const cancelButton = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    const previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    cancelButton.current?.focus();
    return () => previousFocus?.focus();
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !busy) {
        event.preventDefault();
        onCancel();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [open, busy, onCancel]);

  if (!open) return null;

  const cancel = () => {
    if (!busy) onCancel();
  };

  // Only a click on the backdrop itself cancels; clicks inside the card
  // bubble up to the overlay but must not dismiss the dialog.
  const handleOverlayClick = (event: MouseEvent<HTMLDivElement>) => {
    if (event.target === event.currentTarget) cancel();
  };

  return (
    <div className="modal-overlay" role="presentation" onClick={handleOverlayClick}>
      <div
        className="modal-card"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={messageId}
      >
        <div className="modal-header">
          <h3 id={titleId}>{title}</h3>
        </div>
        <div className="modal-body">
          <p id={messageId} className="confirm-message">{message}</p>
        </div>
        <div className="modal-footer">
          <button
            ref={cancelButton}
            type="button"
            className="btn btn-secondary"
            onClick={cancel}
            disabled={busy}
          >
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-danger"
            onClick={() => void onConfirm()}
            disabled={busy}
          >
            {busy ? 'Deleting…' : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
