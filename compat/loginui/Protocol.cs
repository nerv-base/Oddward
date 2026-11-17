// SPDX-License-Identifier: GPL-3.0-or-later
using System.IO;
using System.Buffers.Binary;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;
namespace Oddward.LoginUi;
internal sealed record Section(string Title = "", string Body = "");
internal sealed record Account(string Id = "", string Label = "", string MaskedPhone = "")
{
 public string Primary => Label;
 public string Secondary => MaskedPhone;
 public string Source => "本机";
 public string AccessibilityLabel => Label + " " + MaskedPhone;
}
internal sealed record Model
{
 public uint GameProcessId { get; init; }
 public string GameCreationTime { get; init; } = "";
 public string Screen { get; init; } = "";
 public string Title { get; init; } = "";
 public string Subtitle { get; init; } = "";
 public string Status { get; init; } = "";
 public bool Busy { get; init; }
 public string PrimaryLabel { get; init; } = "";
 public string SmsLabel { get; init; } = "";
 public bool CanSendSms { get; init; }
 public bool Remember { get; init; }
 public bool CanBack { get; init; }
 public bool CanOfficial { get; init; }
 public Section[] Notice { get; init; } = [];
 public Account[] Accounts { get; init; } = [];
 public string SelectedAccountId { get; init; } = "";
}
internal sealed record UiEvent(string Kind, string Phone = "", string Captcha = "", string AccountId = "", string Label = "", bool Remember = false, string? Window = null, string? GameWindow = null, string? RequestId = null);
internal sealed record Envelope(int Version = 1, string Type = "", Model? Model = null, UiEvent? Event = null);
internal static class Protocol
{
 internal const int MaxFrame = 65536;
 private static readonly JsonSerializerOptions Json = new() {
  PropertyNamingPolicy=JsonNamingPolicy.CamelCase,
  UnmappedMemberHandling=JsonUnmappedMemberHandling.Disallow,
  DefaultIgnoreCondition=JsonIgnoreCondition.WhenWritingNull,
  IgnoreReadOnlyProperties=true,
  MaxDepth=16
 };
 internal static Envelope? Read(Stream stream)
 {
  Span<byte> header=stackalloc byte[4];int first=stream.ReadByte();if(first<0)return null;header[0]=(byte)first;
  byte[] data=[];
  try {
   stream.ReadExactly(header[1..]);uint count=BinaryPrimitives.ReadUInt32LittleEndian(header);
   if(count==0||count>MaxFrame)throw Invalid();
   data=new byte[count];stream.ReadExactly(data);
   Envelope? result=JsonSerializer.Deserialize<Envelope>(data,Json);
   if(result is null||!Valid(result))throw Invalid();
   if(result.Event is {Kind:not ("bind_overlay" or "embed_overlay")}) {
    // Null and empty HWND fields are still forbidden on unrelated wire events.
    using JsonDocument document=JsonDocument.Parse(data,new JsonDocumentOptions{MaxDepth=16});
    JsonElement intent=document.RootElement.GetProperty("event");
    if(intent.TryGetProperty("window",out _)||intent.TryGetProperty("gameWindow",out _)||intent.TryGetProperty("requestId",out _))throw Invalid();
   }
   return result;
  } catch(Exception e) when(e is JsonException or EndOfStreamException or ArgumentException) {throw Invalid();}
  finally {CryptographicOperations.ZeroMemory(data);}
 }
 internal static void Write(Stream stream, Envelope frame)
 {
  if(!Valid(frame))throw Invalid();byte[] data=JsonSerializer.SerializeToUtf8Bytes(frame,Json);
  try {
   if(data.Length==0||data.Length>MaxFrame)throw Invalid();
   Span<byte> header=stackalloc byte[4];BinaryPrimitives.WriteUInt32LittleEndian(header,(uint)data.Length);
   stream.Write(header);stream.Write(data);stream.Flush();
  } finally {CryptographicOperations.ZeroMemory(data);}
 }
 private static InvalidDataException Invalid()=>new("Login UI protocol rejected.");
 private static bool Bound(string? s,int max)=>s is not null&&Encoding.UTF8.GetByteCount(s)<=max;
 private static bool Valid(Envelope e)=>e.Version==1&&(e.Type switch {
  "close"=>e.Model is null&&e.Event is null,
  "event"=>e.Model is null&&e.Event is not null&&ValidEvent(e.Event),
  "window_bound"=>e.Model is null&&e.Event is {Kind:"bind_overlay" or "embed_overlay"}&&ValidEvent(e.Event),
  "model"=>e.Event is null&&e.Model is not null&&ValidModel(e.Model),
  _=>false
 });
 private static bool ValidEvent(UiEvent e) {
  if(!Bound(e.Phone,32)||!Bound(e.Captcha,16)||!Bound(e.AccountId,64)||!Bound(e.Label,128))return false;
  if(e.Kind is "bind_overlay" or "embed_overlay")return CanonicalPositive(e.Window)&&CanonicalPositive(e.GameWindow)&&CanonicalPositive(e.RequestId)&&e.Phone==""&&e.Captcha==""&&e.AccountId==""&&e.Label==""&&!e.Remember;
  if(e.Window is not null||e.GameWindow is not null||e.RequestId is not null)return false;
  return e.Kind switch {
   "ready" or "acknowledge" or "official" or "cancel" or "help" or "back" or "other_account"=>e.Phone==""&&e.Captcha==""&&e.AccountId==""&&e.Label==""&&!e.Remember,
   "send_sms"=>e.Captcha==""&&e.AccountId==""&&e.Label==""&&!e.Remember,
   "login_phone"=>e.AccountId==""&&e.Label=="",
   "select_account" or "login_saved" or "delete_account"=>e.AccountId!=""&&e.Phone==""&&e.Captcha==""&&e.Label==""&&!e.Remember,
   "rename_account"=>e.AccountId!=""&&e.Phone==""&&e.Captcha==""&&!e.Remember,
   _=>false
  };
 }
 private static bool CanonicalPositive(string? value)=>value is {Length:>0 and <=20}&&value[0] is >= '1' and <= '9'&&value.All(c=>c is >= '0' and <= '9')&&
  ulong.TryParse(value,System.Globalization.NumberStyles.None,System.Globalization.CultureInfo.InvariantCulture,out _);
 private static bool ValidIdentity(Model m)=>m.GameProcessId==0 ? m.GameCreationTime=="" : CanonicalPositive(m.GameCreationTime);
 private static bool ValidModel(Model m)=>
  ValidIdentity(m)&&m.Screen is "notice" or "phone" or "accounts"&&Bound(m.Title,256)&&Bound(m.Subtitle,512)&&Bound(m.Status,4096)&&Bound(m.PrimaryLabel,128)&&Bound(m.SmsLabel,128)&&Bound(m.SelectedAccountId,64)&&
  m.Accounts is {Length:<=16}&&m.Accounts.All(a=>a is not null&&a.Id!=""&&Bound(a.Id,64)&&Bound(a.Label,128)&&Bound(a.MaskedPhone,32))&&
  m.Notice is {Length:<=8}&&m.Notice.All(s=>s is not null&&Bound(s.Title,256)&&Bound(s.Body,4096));
}
