import { useForm } from "react-hook-form";

export function CheckoutForm({ total, age }: { total: number; age: number }) {
  const { register } = useForm();
  return (
    <form>
      <input type="number" {...register("quantity", { required: true, max: 20 })} />
      {total > 10000 && <ApprovalNotice />}
      <button disabled={age < 21}>Buy</button>
    </form>
  );
}
