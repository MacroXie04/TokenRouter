import {
  type CheckoutResult
} from './wallet-api';

export function CheckoutControl({ checkout, label }: { checkout: CheckoutResult; label: string }) {
  if (checkout.method === 'POST') {
    return (
      <form
        aria-label={label}
        action={checkout.action}
        method="post"
        acceptCharset="UTF-8"
      >
        {checkout.fields.map((field) => (
          <input key={field.name} type="hidden" name={field.name} value={field.value} />
        ))}
        <button type="submit" className="button">{label}</button>
      </form>
    );
  }
  return (
    <a className="button" href={checkout.action} target="_blank" rel="noopener noreferrer">{label}</a>
  );
}
