// SPDX-License-Identifier: GPL-3.0-or-later
using System.Windows;
using System.Windows.Automation;
using System.Windows.Controls;
using System.Windows.Media;
using System.Windows.Media.Animation;
using System.Windows.Media.Effects;
using System.Windows.Media.Imaging;
using System.Windows.Shapes;
using System.Windows.Input;
using System.Windows.Interop;
using System.Windows.Threading;
using Nte.LoginHost.Presentation;
using System.Runtime.InteropServices;

namespace Oddward.LoginUi;

// No network, persistence, credential store, or game-process access.
internal sealed class LoginWindow : Window
{
 private readonly Action<UiEvent> emit;
 private readonly TextBlock title=new(),subtitle=new(),status=new();
 private readonly StackPanel form=new(),footer=new();
 private readonly Button close=Button("close","关闭"),primary=Button("primary","登录"),other=Button("other_account","其他账号"),official=Button("official","使用官方启动器");
 private readonly CheckBox remember=new(){Content="在此设备保存账号"};
 private readonly TextBox phone=new(){MaxLength=11,IsUndoEnabled=false},captcha=new(){MaxLength=8,IsUndoEnabled=false};
 private readonly Button sms=Button("send_sms","发送验证码");
 private readonly ListBox accounts=new(){Style=LoginVisuals.Style("SavedAccountListStyle"),MaxHeight=164};
 private readonly LoginSurface surface;
 private readonly StackPanel phoneGroup,captchaGroup;
 private Model model=new();
 private bool pending,applying,hasModel;
 private string focusedInput="";
 private FormState? destroyedForm;
 private readonly TextBlock bannerText=new();
 private readonly DispatcherTimer bannerTimer=new();
 private bool bannerMode,bannerStarted,dismissed;
 // Anomaly Eye from internal/branding/symbol.svg: the four closed subpaths,
 // constant with the artwork, offset by the asset's own translate group.
 private static readonly Geometry Mark=CreateMark();
 private static Geometry CreateMark()
 {
  // Geometry.Parse returns a frozen path, so the asset's own translate group
  // becomes a wrapper instead of a property write.
  GeometryGroup mark=new(){Transform=new TranslateTransform(-121,-63)};
  mark.Children.Add(Geometry.Parse("M 264 435 L 357 259 L 800 143 L 765 222 L 815 247 L 790 302 L 826 334 L 799 414 C 565 373 468 370 264 435 Z M 184 298 L 281 367 L 250 440 L 518 516 L 237 615 Z M 333 606 L 742 425 L 803 581 C 605 599 438 662 279 751 Z M 553 412 L 609 409 L 611 465 L 554 467 Z"));
  mark.Freeze();
  return mark;
 }
 internal bool CloseRequested {get;private set;}
 internal bool InfrastructureLost {get;private set;}
 protected override void OnSourceInitialized(EventArgs e)
 {
  base.OnSourceInitialized(e);
  HwndSource.FromHwnd(new WindowInteropHelper(this).Handle)?.AddHook(OnNativeMessage);
 }
 private nint OnNativeMessage(nint hwnd,int message,nint wParam,nint lParam,ref bool handled)
 {
  // Parent destruction delivers WM_DESTROY before Closed, but NCDESTROY after
  // Closed. Preserve the form while controls still have their original state.
  if(message==0x0002&&!CloseRequested){destroyedForm=CaptureForm();InfrastructureLost=true;}
  return 0;
 }
 protected override void OnClosing(System.ComponentModel.CancelEventArgs e)
 {
  base.OnClosing(e);if(!e.Cancel)CloseRequested=true;
 }
 internal LoginWindow(Action<UiEvent> emit)
 {
  this.emit=emit;
  Title="Oddward 国服登录";Width=420;Height=520;ResizeMode=ResizeMode.NoResize;
  Icon=BitmapFrame.Create(new Uri("pack://application:,,,/Branding/icon.ico",UriKind.Absolute));
  WindowStyle=WindowStyle.None;AllowsTransparency=true;Background=Brushes.Transparent;
  WindowStartupLocation=WindowStartupLocation.CenterScreen;
  Resources=LoginVisuals.Create();
  surface=LoginSurface.Compose(new LoginParts(title,subtitle,form,footer,status,close,primary,other,official,remember,"登录说明","返回"));
  Content=surface.Card;
  AutomationProperties.SetAutomationId(phone,"phone");AutomationProperties.SetAutomationId(captcha,"captcha");
  AutomationProperties.SetAutomationId(accounts,"accounts");AutomationProperties.SetName(accounts,"保存的账号");
  phoneGroup=LoginSurface.Group("手机号",LoginSurface.Input(phone,"请输入手机号",prefix:"+86"));
  captchaGroup=LoginSurface.Group("验证码",LoginSurface.Input(captcha,"请输入验证码",sms));
  InputMethod.SetIsInputMethodEnabled(phone,false);InputMethod.SetIsInputMethodEnabled(captcha,false);
  phone.GotKeyboardFocus+=(_,_)=>focusedInput="phone";captcha.GotKeyboardFocus+=(_,_)=>focusedInput="captcha";
  close.Click+=(_,_)=>Close();
  bannerTimer.Tick+=(_,_)=>Dismiss();

  primary.Click+=(_,_)=>Primary();
  sms.Click+=(_,_)=>{if(model.Screen=="phone"&&model.CanSendSms)Send(new("send_sms",Phone:phone.Text));};
  other.Click+=(_,_)=>Send(new("other_account"));
  official.Click+=(_,_)=>{if(model.CanOfficial)Send(new("official"));};
  surface.Help.Click+=(_,_)=>Send(new("help"));
  surface.Back.Click+=(_,_)=>{if(model.CanBack)Send(new("back"));};
  accounts.SelectionChanged+=(_,_)=>{if(!applying&&accounts.SelectedItem is Account a)Send(new("select_account",AccountId:a.Id));};
 }
 // This snapshot stays in memory only and has no generated value-printing ToString.
 internal sealed class FormState
 {
  internal string Screen="",Phone="",Captcha="",FocusedInput="";
  internal int PhoneStart,PhoneLength,CaptchaStart,CaptchaLength;
  internal bool Remember,Pending;
 }
 internal FormState CaptureForm()=>destroyedForm??new(){Screen=model.Screen,Phone=phone.Text,Captcha=captcha.Text,Remember=remember.IsChecked==true,PhoneStart=phone.SelectionStart,PhoneLength=phone.SelectionLength,CaptchaStart=captcha.SelectionStart,CaptchaLength=captcha.SelectionLength,FocusedInput=focusedInput,Pending=pending};
 internal void RestoreForm(FormState state)
 {
  if(state.Screen!=model.Screen)return;
  phone.Text=state.Phone;captcha.Text=state.Captcha;remember.IsChecked=state.Remember;
  phone.Select(state.PhoneStart,state.PhoneLength);captcha.Select(state.CaptchaStart,state.CaptchaLength);
  pending=state.Pending;focusedInput=state.FocusedInput;SetEnabled();
  // Restore logical focus without activating the game or another application's
  // foreground window. The normal input route restores keyboard focus on entry.
  if(focusedInput=="phone")FocusManager.SetFocusedElement(this,phone);
  else if(focusedInput=="captcha")FocusManager.SetFocusedElement(this,captcha);
 }
 internal void UseGameOverlay()
 {
  // The greeting banner already owns the window surface. GameOverlay applies
  // the overlay mode right after Apply, so it must not replace the banner.
  if(bannerMode)return;
  UseGameOverlay(SystemParameters.HighContrast,TransparencyEnabled());
 }
 private static bool TransparencyEnabled()
 {
  try {
   // Public Windows UI preference only; no account or game configuration.
   using Microsoft.Win32.RegistryKey? key=Microsoft.Win32.Registry.CurrentUser.OpenSubKey(@"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize",writable:false);
   return key?.GetValue("EnableTransparency",1) is not int value||value!=0;
  } catch(Exception error) when(error is System.Security.SecurityException or UnauthorizedAccessException or System.IO.IOException) {return false;}
 }
 internal void UseGameOverlay(bool highContrast,bool transparencyEnabled)
 {
  ShowInTaskbar=false;ShowActivated=false;Topmost=false;
  WindowStartupLocation=WindowStartupLocation.Manual;
  Content=null;
  Viewbox card=new(){Child=surface.Card,Stretch=Stretch.Uniform,HorizontalAlignment=HorizontalAlignment.Center,VerticalAlignment=VerticalAlignment.Center};
  Grid mask=new(){Background=new SolidColorBrush(Color.FromArgb(highContrast||!transparencyEnabled?(byte)255:(byte)166,0,0,0)),ClipToBounds=true};
  mask.Children.Add(card);Content=mask;
  mask.SizeChanged+=(_,_)=>{card.Width=mask.ActualWidth*.9;card.Height=mask.ActualHeight*.8;};
 }
 internal void Apply(Model value)
 {
  Dispatcher.VerifyAccess();
  if(value.Screen=="welcome"){EnterBanner(value,SystemParameters.HighContrast,TransparencyEnabled());return;}
  applying=true;
  try {
   // Same-screen countdown snapshots must not replace unsubmitted local input.
   if(!hasModel||model.Screen!=value.Screen){phone.Clear();captcha.Clear();remember.IsChecked=value.Remember;}
   model=value;hasModel=true;pending=false;
   title.Text=value.Title.Length>0?value.Title:value.Screen switch{"notice"=>"登录前请阅读","accounts"=>"选择保存的账号",_=>"手机号登录"};
   subtitle.Text=value.Subtitle;status.Text=value.Status;
   primary.Content=value.PrimaryLabel.Length>0?value.PrimaryLabel:value.Screen=="notice"?"我已了解并继续":"登录";
   AutomationProperties.SetName(primary,(string)primary.Content);
   sms.Content=value.SmsLabel.Length>0?value.SmsLabel:"发送验证码";
   AutomationProperties.SetName(sms,(string)sms.Content);
   official.Visibility=value.CanOfficial?Visibility.Visible:Visibility.Collapsed;
   other.Visibility=value.Screen=="accounts"?Visibility.Visible:Visibility.Collapsed;
   close.Visibility=value.Screen=="notice"?Visibility.Collapsed:Visibility.Visible;
   form.Children.Clear();
   switch(value.Screen) {
    case "notice":
     foreach(Section section in value.Notice){form.Children.Add(Text(section.Title,true));form.Children.Add(Text(section.Body));}
     break;
    case "phone":form.Children.Add(phoneGroup);form.Children.Add(captchaGroup);break;
    case "accounts":
     accounts.ItemsSource=value.Accounts;accounts.SelectedItem=value.Accounts.FirstOrDefault(a=>a.Id==value.SelectedAccountId);
     form.Children.Add(accounts);
     StackPanel tools=new(){Orientation=Orientation.Horizontal,HorizontalAlignment=HorizontalAlignment.Center};
     Button rename=Button("rename","重命名"),delete=Button("delete","删除账号");
     rename.Style=delete.Style=LoginVisuals.Style(LoginVisuals.LinkButton);
     rename.Click+=(_,_)=>{if(CanAct&&Selected() is not null)Edit(false);};
     delete.Click+=(_,_)=>{if(CanAct&&Selected() is not null)Edit(true);};
     rename.IsEnabled=delete.IsEnabled=Selected() is not null;
     tools.Children.Add(rename);tools.Children.Add(delete);form.Children.Add(tools);break;
   }
   surface.SetPresentation(value.Screen=="notice",value.Screen=="accounts",false,value.CanBack);
   SetEnabled();
  } finally {applying=false;}
 }
 private bool CanAct=>!pending&&!model.Busy&&!applying;
 internal void EnterBanner(Model value,bool highContrast,bool transparencyEnabled)
 {
  model=value;hasModel=true;bannerText.Text=value.Banner;
  AutomationProperties.SetName(bannerText,value.Banner);
  if(bannerMode)return;
  bannerMode=true;
  UseGameOverlay(highContrast,transparencyEnabled);
  Color body=LoginVisuals.Brush(LoginVisuals.Body) is SolidColorBrush solid?solid.Color:Color.FromRgb(0xE3,0xE3,0xE3);
  bannerText.Foreground=highContrast?SystemColors.WindowTextBrush:LoginVisuals.Brush(LoginVisuals.Text);
  bannerText.FontFamily=LoginVisuals.Font;bannerText.FontSize=15;bannerText.FontWeight=FontWeights.SemiBold;
  bannerText.TextWrapping=TextWrapping.NoWrap;bannerText.TextTrimming=TextTrimming.CharacterEllipsis;
  AutomationProperties.SetAutomationId(bannerText,"banner");
  // The mark is the project's own Anomaly Eye (symbol.svg) as closed-form
  // geometry, so it takes the same monochrome foreground as the text and
  // stays crisp at any DPI without decoding an image.
  Path mark=new(){
   Data=Mark,Stretch=Stretch.Uniform,Fill=bannerText.Foreground,Width=20,Height=20,
   VerticalAlignment=VerticalAlignment.Center,Margin=new Thickness(0,0,9,0)};
  AutomationProperties.SetAutomationId(mark,"banner_mark");AutomationProperties.SetName(mark,"Oddward");
  Grid row=new();
  row.ColumnDefinitions.Add(new ColumnDefinition{Width=GridLength.Auto});
  row.ColumnDefinitions.Add(new ColumnDefinition{Width=new GridLength(1,GridUnitType.Star)});
  Grid.SetColumn(mark,0);Grid.SetColumn(bannerText,1);
  row.Children.Add(mark);row.Children.Add(bannerText);
  Border pill=new(){
   Tag="pill",Child=row,Padding=new Thickness(18,10,22,10),BorderThickness=new Thickness(1),
   Background=new SolidColorBrush(Color.FromArgb(highContrast||!transparencyEnabled?(byte)255:(byte)235,body.R,body.G,body.B)),
   BorderBrush=highContrast?SystemColors.WindowTextBrush:LoginVisuals.Brush(LoginVisuals.Bezel),
   IsHitTestVisible=false,
   HorizontalAlignment=HorizontalAlignment.Center,VerticalAlignment=VerticalAlignment.Top,Margin=new Thickness(0,48,0,0),
   Effect=new DropShadowEffect{BlurRadius=18,ShadowDepth=3,Direction=270,Opacity=.35,Color=Colors.Black}};
  AutomationProperties.SetAutomationId(pill,"banner_card");
  Grid root=new(){Background=Brushes.Transparent};root.Children.Add(pill);Content=root;
  // Keep a renamed account inside a narrow game client instead of growing
  // past its edge; TextBlock trims with an ellipsis once the cap arrives.
  root.SizeChanged+=(_,e)=>pill.MaxWidth=Math.Max(0,e.NewSize.Width-48);
  SetBannerWindowStyles();
  IsVisibleChanged+=(_,_)=>{if(IsVisible)BeginBanner();};
 }
 private const long WsExTransparent=0x00000020,WsExToolWindow=0x00000080,WsExNoActivate=0x08000000;
 [DllImport("user32.dll",EntryPoint="GetWindowLongPtrW")]
 private static extern nint GetWindowLongPtr(nint hwnd,int index);
 [DllImport("user32.dll",EntryPoint="SetWindowLongPtrW",SetLastError=true)]
 private static extern nint SetWindowLongPtr(nint hwnd,int index,nint value);
 private void SetBannerWindowStyles()
 {
  // Decorating a game window must never take focus, appear on the taskbar,
  // or intercept a click meant for the game.
  nint hwnd=new WindowInteropHelper(this).EnsureHandle();
  nint current=GetWindowLongPtr(hwnd,-20);
  SetWindowLongPtr(hwnd,-20,(nint)(current.ToInt64()|WsExTransparent|WsExToolWindow|WsExNoActivate));
 }
 private void BeginBanner()
 {
  if(bannerStarted)return;bannerStarted=true;
  if(!SystemParameters.ClientAreaAnimation){bannerTimer.Interval=TimeSpan.FromSeconds(2.4);bannerTimer.Start();return;}
  DoubleAnimationUsingKeyFrames fade=new();
  fade.KeyFrames.Add(new LinearDoubleKeyFrame(0,KeyTime.FromTimeSpan(TimeSpan.Zero)));
  fade.KeyFrames.Add(new EasingDoubleKeyFrame(1,KeyTime.FromTimeSpan(TimeSpan.FromMilliseconds(220)),new CubicEase{EasingMode=EasingMode.EaseOut}));
  fade.KeyFrames.Add(new LinearDoubleKeyFrame(1,KeyTime.FromTimeSpan(TimeSpan.FromMilliseconds(2620))));
  fade.KeyFrames.Add(new EasingDoubleKeyFrame(0,KeyTime.FromTimeSpan(TimeSpan.FromMilliseconds(3000)),new CubicEase{EasingMode=EasingMode.EaseIn}));
  BeginAnimation(OpacityProperty,fade);
  bannerTimer.Interval=TimeSpan.FromMilliseconds(3050);bannerTimer.Start();
 }
 private void Dismiss()
 {
  if(dismissed)return;dismissed=true;
  bannerTimer.Stop();
  try{Close();}catch(InvalidOperationException){CloseRequested=true;}
 }
 private Account? Selected()=>accounts.SelectedItem as Account;
 private void Primary()
 {
  if(!CanAct)return;
  switch(model.Screen) {
   case "notice":Send(new("acknowledge"));break;
   case "phone":
    UiEvent e=new("login_phone",Phone:phone.Text,Captcha:captcha.Text,Remember:remember.IsChecked==true);
    phone.Clear();captcha.Clear();Send(e);break;
   case "accounts":if(Selected() is Account a)Send(new("login_saved",AccountId:a.Id));break;
  }
 }
 private void Send(UiEvent e)
 {
  if(!CanAct)return;
  pending=true;SetEnabled();emit(e);
 }
 private void SetEnabled()
 {
  bool enabled=!model.Busy&&!pending;
  form.IsEnabled=enabled;primary.IsEnabled=enabled&&(model.Screen!="accounts"||Selected() is not null);
  other.IsEnabled=official.IsEnabled=remember.IsEnabled=surface.Help.IsEnabled=surface.Back.IsEnabled=enabled;
  sms.IsEnabled=enabled&&model.CanSendSms;
 }
 private void Edit(bool deletion)
 {
  Account? a=Selected();if(a is null)return;
  form.Children.Clear();form.Children.Add(Text(deletion?"删除本机保存的账号？":"修改账号名称",true));
  form.Children.Add(Text(a.MaskedPhone.Length==0?a.Label:a.Label+" · "+a.MaskedPhone));
  TextBox label=new(){Text=a.Label,MaxLength=32,IsUndoEnabled=false};AutomationProperties.SetAutomationId(label,"account_label");
  if(!deletion)form.Children.Add(LoginSurface.Group("账号名称",LoginSurface.Input(label,"账号名称")));
  Button confirm=Button(deletion?"confirm_delete":"confirm_rename",deletion?"确认删除":"保存名称");
  confirm.Style=LoginVisuals.Style(LoginVisuals.SecondaryButton);
  confirm.Click+=(_,_)=>Send(new(deletion?"delete_account":"rename_account",AccountId:a.Id,Label:deletion?"":label.Text));
  form.Children.Add(confirm);
  Button cancel=Button("cancel_edit","取消");cancel.Style=LoginVisuals.Style(LoginVisuals.LinkButton);cancel.Click+=(_,_)=>Apply(model);form.Children.Add(cancel);
  primary.IsEnabled=false;
 }
 private static Button Button(string id,string text)
 {
  Button b=new(){Content=text};AutomationProperties.SetAutomationId(b,id);AutomationProperties.SetName(b,text);return b;
 }
 private static TextBlock Text(string text,bool heading=false)=>new(){Text=text,FontFamily=LoginVisuals.Font,FontSize=heading?14:12.5,FontWeight=heading?FontWeights.Bold:FontWeights.Normal,Foreground=LoginVisuals.Brush(LoginVisuals.Text),TextWrapping=TextWrapping.Wrap,Margin=new Thickness(0,heading?10:4,0,8)};
}
